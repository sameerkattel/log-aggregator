package k8

import (
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	kcache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// Resync period for the kube controller loop.
	resyncPeriod = 30 * time.Minute
)

type tracker interface {
	Get(string, string) *v1.Pod
}

type podTracker struct {
	client *kubernetes.Clientset

	// The name of the node that we are running on.
	NodeName string
	cache    *lru.Cache
}

func newK8(k8ConfigPath string) (*kubernetes.Clientset, error) {
	config, err := clientcmd.BuildConfigFromFlags("", k8ConfigPath)
	if err != nil {
		return nil, errors.Wrap(err, "error getting config from path")
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, errors.Wrap(err, "error constructing k8 client from config")
	}
	return clientset, nil
}

func newPodTracker(client *kubernetes.Clientset, nodeName string, maxPods int) *podTracker {
	cache, err := lru.New(maxPods)
	if err != nil {
		panic(err)
	}
	return &podTracker{
		NodeName: nodeName,
		cache:    cache,
		client:   client,
	}
}

func (t *podTracker) watchForPods() {
	_, podController := kcache.NewInformer(
		kcache.NewListWatchFromClient(t.client.CoreV1().RESTClient(), "pods", v1.NamespaceAll, fields.Everything()),
		&v1.Pod{},
		resyncPeriod,
		kcache.ResourceEventHandlerFuncs{
			AddFunc:    t.OnAdd,
			DeleteFunc: t.OnDelete,
			UpdateFunc: t.OnUpdate,
		},
	)
	go podController.Run(wait.NeverStop)
	return
}

func (t *podTracker) Get(namespaceName, podName string) *v1.Pod {
	if val, ok := t.cache.Get(t.cacheKey(namespaceName, podName)); ok {
		return val.(*v1.Pod)
	}
	pod, err := t.client.CoreV1().Pods(namespaceName).Get(podName, metav1.GetOptions{})
	if err == nil {
		t.cache.ContainsOrAdd(t.cacheKey(namespaceName, podName), pod)
		return pod
	}
	return nil
}

func (t *podTracker) OnAdd(obj interface{}) {
	if pod, ok := obj.(*v1.Pod); ok {
		if t.canTrackPod(pod) {
			t.cache.Add(t.cacheKey(pod.Namespace, pod.Name), pod)
			fmt.Printf("ADD %s : %s\n", pod.Namespace, pod.Name)
		}
	}
}

// Called with two api.Pod objects, with the first being the old version, and
// the second being the new version.
// It is invoked synchronously along with OnAdd and OnDelete.
func (t *podTracker) OnUpdate(oldObj, newObj interface{}) {
	_, ok1 := oldObj.(*v1.Pod)
	newPod, ok2 := newObj.(*v1.Pod)
	if !ok1 || !ok2 {
		return
	}
	if t.canTrackPod(newPod) {
		t.cache.Add(t.cacheKey(newPod.Namespace, newPod.Name), newPod)
		fmt.Printf("UPD %s : %s\n", newPod.Namespace, newPod.Name)
	}
}

// Called with an api.Pod object when the pod has been deleted.
// It is invoked synchronously along with OnAdd and OnUpdate.
func (t *podTracker) OnDelete(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		deletedObj, dok := obj.(kcache.DeletedFinalStateUnknown)
		if dok {
			pod, ok = deletedObj.Obj.(*v1.Pod)
		}
	}
	if !ok {
		return
	}
	t.cache.Remove(t.cacheKey(pod.Namespace, pod.Name))
	fmt.Printf("DEL %s : %s\n", pod.Namespace, pod.Name)
}

func (t *podTracker) cacheKey(namespaceName, podName string) string {
	return namespaceName + "_" + podName
}

func (t *podTracker) canTrackPod(pod *v1.Pod) bool {
	if pod.Spec.NodeName == "" {
		return false
	} else if t.NodeName != "" && t.NodeName != pod.Spec.NodeName {
		return false
	}
	return true
}
