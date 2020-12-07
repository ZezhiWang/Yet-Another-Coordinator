package main

import (
	"encoding/json"
	"net/http"
	"sync"
)

var sagas sync.Map

type Status int

const (
	Initialized Status = iota
	Running
	Failed
	Aborted
	Success
)

type Request struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   string `json:"body"`
	Status Status
}

type Saga struct {
	Client      string
	Leader      string
	Transaction Transaction
	Status      Status
}

type TransactionReq struct {
	PartialReq Request `json:"partial_req"`
	CompReq    Request `json:"comp_req"`
}

type Transaction struct {
	Tiers map[int]map[string]TransactionReq `json:"tier"`
}

type PartialResponse struct {
	SagaId string
	Tier   int
	ReqID  string
	IsComp bool // true -> compensate req, false -> partial req
	Status Status
}

/*
This creates a saga from a request. Requests should be of the following format:

{
	"tier": {
		"0": {
			"<request_name>": {
				"partial_req": {"method": "POST", "url": "/test2/", "body": "test2"},
				"comp_req": {"method": "POST", "url": "/test3/", "body": "test3"}
			},
			"<request_name>": {
				"partial_req": {"method": "POST", "url": "/test5/", "body": "test5"},
				"comp_req": {"method": "POST", "url": "/test5/", "body": "test5"}
			}
		},
		"1": {
			"<request_name>": {
				"partial_req": {"method": "POST", "url": "/test5/", "body": "test5"},
				"comp_req": {"method": "POST", "url": "/test5/", "body": "test5"}
			}
		}
 	}
}

For testing, see: https://play.golang.org/p/dsVLkT176mo
*/
func getSagaFromReq(req *http.Request, leader string) (Saga, error) {
	var transaction Transaction

	// Decoded request body into transaction struct. If there is an error,
	// respond to the client with the error message and a 400 status code.
	err := json.NewDecoder(req.Body).Decode(&transaction)
	if err != nil {
		return Saga{}, err
	}

	// construct Saga
	return Saga{
		Client:      getIpFromAddr(req.RemoteAddr),
		Leader:      leader,
		Transaction: transaction,
		Status:      Initialized,
	}, nil
}

func (s *Saga) toByteArray() []byte {
	arr, _ := json.Marshal(s)
	return arr
}

func fromByteArray(arr []byte) Saga {
	var s Saga
	_ = json.Unmarshal(arr, &s)
	return s
}
