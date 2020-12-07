package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

type MsgStatus struct {
	ok    bool
	reqID string
}

func getIpFromAddr(remoteAddr string) string {
	addrs := strings.Split(remoteAddr, ":")
	return addrs[0]
}

func sendPostMsg(url, reqID string, body []byte, ack chan MsgStatus) {
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	ok := err == nil && resp.StatusCode >= 200 && resp.StatusCode <= 299
	ack <- MsgStatus{ok: ok, reqID: reqID}
}

func sendGetMsg(url, reqID string, ack chan MsgStatus) {
	resp, err := http.Get(url)
	ok := err == nil && resp.StatusCode >= 200 && resp.StatusCode <= 299
	log.Println("GET", url, err)
	ack <- MsgStatus{ok: ok, reqID: reqID}
}

func sendPutMsg(url, reqID string, body []byte, ack chan MsgStatus) {
	client := &http.Client{}
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(body))
	if err != nil {
		ack <- MsgStatus{ok: false, reqID: reqID}
		return
	}

	resp, err := client.Do(req)
	ok := err == nil && resp.StatusCode >= 200 && resp.StatusCode <= 299
	ack <- MsgStatus{ok: ok, reqID: reqID}
}

func sendDelMsg(url, reqID string, ack chan MsgStatus) {
	client := &http.Client{}
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		ack <- MsgStatus{ok: false, reqID: reqID}
		return
	}
	resp, err := client.Do(req)
	ok := err == nil && resp.StatusCode >= 200 && resp.StatusCode <= 299
	ack <- MsgStatus{ok: ok, reqID: reqID}
}

func sendMessage(reqID string, req Request, resp chan MsgStatus) {
	if req.Method == "POST" {
		body, err := json.Marshal(req.Body)
		if err != nil {
			resp <- MsgStatus{ok: false, reqID: reqID}
		}
		sendPostMsg(req.URL, reqID, body, resp)
	} else if req.Method == "GET" {
		sendGetMsg(req.URL, reqID, resp)
	} else if req.Method == "PUT" {
		body, err := json.Marshal(req.Body)
		if err != nil {
			resp <- MsgStatus{ok: false, reqID: reqID}
		}
		sendPutMsg(req.URL, reqID, body, resp)
	} else if req.Method == "DELETE" {
		sendDelMsg(req.URL, reqID, resp)
	} else {
		resp <- MsgStatus{ok: false, reqID: reqID}
	}
}

// Returns tier that needs to be rolled back to, nil on success
func sendPartialRequests(sagaId string, subCluster []string) (int, bool) {
	sagaI, _ := sagas.Load(sagaId)
	tiersMap := sagaI.(Saga).Transaction.Tiers

	// since maps do not guarantee order, we get keys and sort them
	tiers := make([]int, 0, len(tiersMap))
	for tier := range tiersMap {
		tiers = append(tiers, tier)
	}

	sort.Ints(tiers)

	// loop in order of tier
	for _, tier := range tiers {
		requestsMap := tiersMap[tier]

		// iterate over requests and asynchronously send partial requests
		success := make(chan MsgStatus)
		for reqID, transaction := range requestsMap {
			go sendMessage(reqID, transaction.PartialReq, success)
		}

		// wait for successes from each partial request, quit on failure
		// TODO: add retries / timeouts
		cnt := 0
		for cnt < len(requestsMap) {
			status := <-success
			if status.ok {
				updateSubCluster(sagaId, tier, status.reqID, false, Success, subCluster)
				cnt++
			} else {
				// failure at this tier, need to roll back
				updateSubCluster(sagaId, tier, status.reqID, false, Failed, subCluster)
				return tier, true
			}
		}
	}

	return -1, false
}

// Tier is highest tier through which (inclusive) we need to roll back
func sendCompensatingRequests(sagaId string, maxTier int, subCluster []string) {
	sagaI, _ := sagas.Load(sagaId)
	tiersMap := sagaI.(Saga).Transaction.Tiers

	// since maps do not guarantee order, we get keys and sort them
	tiers := make([]int, 0, len(tiersMap))
	for tier := range tiersMap {
		tiers = append(tiers, tier)
	}

	sort.Ints(tiers)

	// loop in order of tier
	for _, tier := range tiers {
		// we have rolled back all the requests necessary

		if tier > maxTier {
			return
		}
		requestsMap := tiersMap[tier]

		// iterate over requests and asynchronously send partial requests
		success := make(chan MsgStatus)
		for reqID, transaction := range requestsMap {
			go sendMessage(reqID, transaction.CompReq, success)
		}

		// wait for successes from each partial request, quit on failure
		// TODO: add retries / timeouts
		cnt := 0
		for cnt < len(requestsMap) {
			status := <-success
			if status.ok {
				updateSubCluster(sagaId, tier, status.reqID, true, Success, subCluster)
				cnt++
			}
			// TODO: handle unsuccessful compensation
		}
	}
}

func updateSubCluster(sagaId string, tier int, reqID string, isComp bool, status Status, subCluster []string) {
	body, _ := json.Marshal(PartialResponse{
		SagaId: sagaId,
		Tier:   tier,
		ReqID:  reqID,
		IsComp: isComp,
		Status: status,
	})

	ack := make(chan MsgStatus)
	for _, server := range subCluster {
		if server != ip {
			go sendPutMsg(server+"/saga/partial", "", body, ack)
		}
	}

	cnt := 1
	for cnt < subClusterSize/2+1 {
		if (<-ack).ok {
			cnt++
		}
	}

	sagaI,_ := sagas.Load(sagaId)
	saga := sagaI.(Saga)
	request := saga.Transaction.Tiers[tier][reqID]
	if isComp {
		request.CompReq.Status = Success
		request.PartialReq.Status = Aborted
	} else {
		request.PartialReq.Status = Success
	}
	saga.Transaction.Tiers[tier][reqID] = request

	sagas.Store(sagaId, saga)
}

func checkIfNewLeader() {
	coordinatorSet := make(map[string]bool, len(coordinators))
	for _, c := range coordinators {
		coordinatorSet[c] = true
	}

	sagas.Range(func(key, value interface{}) bool {
		s := value.(Saga)
		if _, isIn := coordinatorSet[s.Leader]; isIn {
			newLeader, _ := ring.GetNode(s.Client)
			s.Leader = newLeader

			sagas.Store(key, s)

			if newLeader == ip {
				leadCompensation(s.Client, key.(string))
			}
		}
		return true
	})
}

func leadCompensation(key, sagaId string) {
	subCluster, _ := ring.GetNodes(key, subClusterSize)

	resp := make(chan MsgStatus)
	for _, svr := range subCluster {
		go sendPutMsg(svr + "/saga/elect/" + sagaId, "", make([]byte, 0), resp)
	}

	ack := 1
	dec := 0
	for ack < subClusterSize/2+1 && dec < subClusterSize/2+1 {
		select {
		case status := <-resp:
			if status.ok {
				ack++
			} else {
				dec++
			}
		case <-time.After(pollFrequency/2 * time.Millisecond):
			log.Println("Timeout")
			break
		}
	}

	if ack < subClusterSize/2+1 {
		return
	} else {
		sagaI, _ := sagas.Load(sagaId)
		tiersMap := sagaI.(Saga).Transaction.Tiers

		maxTier := -1
		for n := range tiersMap {
			if n > maxTier {
				for reqID := range tiersMap[n] {
					if tiersMap[n][reqID].PartialReq.Status != Aborted && tiersMap[n][reqID].CompReq.Status != Success {
						maxTier = n
						break
					}
				}
			}
		}

		if maxTier >= 0 {
			sendCompensatingRequests(sagaId, maxTier, subCluster)
		}
	}
}
