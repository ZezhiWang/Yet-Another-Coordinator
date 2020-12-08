package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func welcome(c *gin.Context) {
	fmt.Println("recieved a welcome message...")
	c.Status(http.StatusOK)
}

func processSaga(c *gin.Context) {
	sagaId := c.Param("request")

	server, ok := ring.GetNode(sagaId)
	if ok == false {
		log.Println("Insufficient Correct Nodes")
		log.Println(coordinators)
		c.Status(http.StatusInternalServerError)
		return
	}

	if server != ip {
		log.Printf("%s, %s, %s, %d\n", sagaId, ip, server, len(coordinators))
		log.Println(coordinators)
		c.Redirect(http.StatusTemporaryRedirect, server+"/saga/"+sagaId)
		return
	}

	saga, err := getSagaFromReq(c.Request, ip)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	sagas.Store(sagaId, saga)

	// get sub cluster
	servers, ok := ring.GetNodes(sagaId, subClusterSize)
	if ok == false {
		log.Println("Insufficient Correct Nodes")
		log.Println(coordinators)
		c.Status(http.StatusInternalServerError)
		return
	}

	// send request to sub cluster
	log.Println("Informing subCluster")
	ack := make(chan MsgStatus)
	for _, server := range servers {
		if server != ip {
			go sendPostMsg(server+"/cluster/"+sagaId, "", saga.toByteArray(), ack)
		}
	}

	// wait for majority of ack
	cnt := 1
	for cnt < subClusterSize/2+1 {
		if (<-ack).ok {
			cnt++
		}
	}

	// execute partial requests
	log.Println("Sending partial requests")
	rollbackTier, rollback := sendPartialRequests(sagaId, servers)

	if rollback == true {
		// experiences failure, need to send compensating requests up to tier (inclusive)
		log.Println("Rolling back", rollbackTier)
		sendCompensatingRequests(sagaId, rollbackTier, servers)
		// reply success
		for _, server := range servers {
			if server != ip {
				go sendDelMsg(server+"/saga/"+sagaId, "", ack)
			}
		}

		// wait for majority of ack
		cnt := 1
		for cnt < subClusterSize/2+1 {
			if (<-ack).ok {
				cnt++
			}
		}

		sagas.Delete(sagaId)
		c.Status(http.StatusBadRequest)
	} else {
		// reply success
		for _, server := range servers {
			if server != ip {
				go sendDelMsg(server+"/saga/"+sagaId, "", ack)
			}
		}

		// wait for majority of ack
		cnt := 1
		for cnt < subClusterSize/2+1 {
			if (<-ack).ok {
				cnt++
			}
		}
		sagas.Delete(sagaId)
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
	reqMap := saga.Transaction.Tiers[resp.Tier]
	req := reqMap[resp.ReqID]
	if resp.IsComp {
		req.CompReq.Status = resp.Status
	} else {
		req.PartialReq.Status = resp.Status
	}
	reqMap[resp.ReqID] = req
	saga.Transaction.Tiers[resp.Tier] = reqMap

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
	if _, isIn := sagas.Load(reqID); isIn {
		leader, _ := ring.GetNode(reqID)
		if leader == getIpFromAddr(c.Request.RemoteAddr) {
			c.Status(http.StatusOK)
			return
		}
	}

	c.Status(http.StatusBadRequest)
}
