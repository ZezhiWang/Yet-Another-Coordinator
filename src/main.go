package main

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/serialx/hashring"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var coordinators = []string{}

var ring = hashring.New(coordinators)

var ip = "localhost:8080"

var clientset *kubernetes.Clientset

func main() {
	fmt.Println("starting up coordinator...")

	ip = "http://" + os.Getenv("POD_IP") + ":8080"

	log.Println("pod ip: " + ip)

	// creating k8s client
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	go updateCoordinatorsList()

	router := gin.Default()

	router.GET("/", welcome)

	router.POST("/saga", processSaga)
	router.POST("/saga/cluster/:request", newSaga)
	router.PUT("/saga/partial", partialRequestResponse)
	router.PUT("/saga/elect/:request", voteAbort)
	router.DELETE("/saga/:request", delSaga)

	if err := router.Run(":8080"); err != nil {
		panic(err)
	}
}

func updateCoordinatorsList() {
	for {
		time.Sleep(pollFrequency * time.Millisecond)

		newCoordinators := pullCoordinators()
		if !reflect.DeepEqual(newCoordinators, coordinators) {
			// update ring
			coordinators = newCoordinators
			ring = hashring.New(coordinators)
			checkIfNewLeader()
		}
	}
}

func pullCoordinators() []string {
	// pull coordinator pods from k8s (includes pod for the current coordinator)
	pods, err := clientset.CoreV1().Pods("yac").List(metav1.ListOptions{})

	if err != nil {
		panic(err.Error())
	}

	//addresses := make([]string, pods.Size())
	//for index, pod := range pods.Items {
	//	addresses[index] = "http://" + pod.Status.PodIP + ":8080"
	//}

	addresses := make([]string, 0)
	for _, pod := range pods.Items {
		if pod.Status.PodIP != "" {
			addresses = append(addresses, "http://" + pod.Status.PodIP + ":8080")
		}
	}

	sort.Strings(addresses)
	return addresses
}
