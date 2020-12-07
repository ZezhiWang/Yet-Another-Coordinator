package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
)

func welcome(c *gin.Context) {
	fmt.Println("recieved a welcome message...")
	c.Status(http.StatusOK)
}

func processSaga(c *gin.Context) {
	key := getIpFromAddr(c.Request.RemoteAddr)
	server, ok := ring.GetNode(key)
	if ok == false {
		log.Fatal("Insufficient Correct Nodes")
	}

	if server != ip {
		log.Printf("%s, %s, %s, %d\n", key, ip, server, len(coordinators))
		log.Println(coordinators)
		c.Redirect(http.StatusTemporaryRedirect, server + "/saga")
		return
	}

	saga, err := getSagaFromReq(c.Request, ip)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	reqID := xid.New().String()

	sagas.Store(reqID, saga)

	// get sub cluster
	servers, ok := ring.GetNodes(key, subClusterSize)
	if ok == false {
		log.Fatal("Insufficient Correct Nodes")
	}

	// send request to sub cluster
	log.Println("Informing subCluster")
	ack := make(chan MsgStatus)
	for _, server := range servers {
		if server != ip {
			go sendPostMsg(server+"/saga/cluster/"+reqID, "", saga.toByteArray(), ack)
		}
	}

	// wait for majority of ack
	cnt := 1
	for cnt < len(servers)/2+1 {
		if (<-ack).ok {
			cnt++
		}
	}

	// execute partial requests
	log.Println("Sending partial requests")
	rollbackTier, rollback := sendPartialRequests(reqID, servers)

	if rollback == true {
		// experiences failure, need to send compensating requests up to tier (inclusive)
		log.Println("Rolling back", rollbackTier)
		sendCompensatingRequests(reqID, rollbackTier, servers)
		// reply success
		for _, server := range servers {
			if server != ip {
				go sendDelMsg(server+"/saga/"+reqID, "", ack)
			}
		}

		// wait for majority of ack
		cnt := 1
		for cnt < len(servers)/2+1 {
			if (<-ack).ok {
				cnt++
			}
		}

		sagas.Delete(reqID)
		c.Status(http.StatusBadRequest)
	} else {
		// reply success
		for _, server := range servers {
			if server != ip {
				go sendDelMsg(server+"/saga/"+reqID, "", ack)
			}
		}

		// wait for majority of ack
		cnt := 1
		for cnt < len(servers)/2+1 {
			if (<-ack).ok {
				cnt++
			}
		}
		sagas.Delete(reqID)
		// TODO: return with body
		c.Status(http.StatusOK)
	}
}

func newSaga(c *gin.Context) {
	reqID := c.Param("request")

	defer c.Request.Body.Close()
	body, _ := ioutil.ReadAll(c.Request.Body)

	sagas.Store(reqID, fromByteArray(body))
	c.Status(http.StatusOK)
}

func partialRequestResponse(c *gin.Context) {
	var resp PartialResponse
	var targetPartialRequest TransactionReq

	body, _ := ioutil.ReadAll(c.Request.Body)
	if err := json.Unmarshal(body, &resp); err != nil {
		log.Println(err)
		c.Status(http.StatusBadRequest)
		return
	}

	sagaI, err := sagas.Load(resp.SagaId)
	if !err {
		c.Status(http.StatusOK)
		return
	}

	saga := sagaI.(Saga)
	saga.Leader = getIpFromAddr(c.Request.RemoteAddr)

	targetPartialRequest = saga.Transaction.Tiers[resp.Tier][resp.ReqID]
	if resp.IsComp {
		targetPartialRequest.CompReq.Status = resp.Status
	} else {
		targetPartialRequest.PartialReq.Status = resp.Status
	}
	saga.Transaction.Tiers[resp.Tier][resp.ReqID] = targetPartialRequest

	sagas.Store(resp.SagaId, saga)

	c.Status(http.StatusOK)
}

func delSaga(c *gin.Context) {
	reqID := c.Param("request")
	sagas.Delete(reqID)
	c.Status(http.StatusOK)
}

func voteAbort(c *gin.Context) {
	reqID := c.Param("request")
	if sagaI, isIn := sagas.Load(reqID); isIn {
		client := sagaI.(Saga).Client
		leader, _ := ring.GetNode(client)
		if leader == getIpFromAddr(c.Request.RemoteAddr) {
			c.Status(http.StatusOK)
			return
		}
	}

	c.Status(http.StatusBadRequest)
}
