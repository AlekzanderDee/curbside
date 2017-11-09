package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"sort"
	"time"
)

type Session struct {
	SessionID string `json:"session"`
}

type CurbIDResponse struct {
	ID         string      `json:"id"`
	Depth      int         `json:"depth"`
	Secret     string      `json:"secret"`
	Message    string      `json:"message"`
	Next       NextWrapper `json:"next"`
	ParentID   string
	OrderIndex int
}

type NextWrapper struct {
	IDs []string
}

func (w *NextWrapper) UnmarshalJSON(data []byte) error {
	// the "next" field can come as a string or as a []string value
	// we force it to be []string:
	var nextList = make([]string, 0)
	err := json.Unmarshal(data, &nextList)
	if err != nil {
		var nextString = ""
		err = json.Unmarshal(data, &nextString)
		if err != nil {
			return err
		}
		w.IDs = []string{nextString}
		return nil
	}
	w.IDs = nextList
	return nil
}

type Secret struct {
	ID         string
	Value      string
	OrderIndex int
}

const SESSION_URL = "http://challenge.curbside.com/get-session"
const ID_URL = "http://challenge.curbside.com/"

// allow only 16 simultaneous requests
var requestsChan = make(chan bool, 16)

// create a buffered channel with buffer size of 8000. this channel is used as a requests limiter.
// if create unbuffered channel then sender will block because on request may provide multiple results
var resultsChan = make(chan CurbIDResponse, 8000)

// setting up shared HTTP transport and client
var netTransport = &http.Transport{
	Dial: (&net.Dialer{
		Timeout: 10 * time.Second,
	}).Dial,
	TLSHandshakeTimeout: 10 * time.Second,
	MaxIdleConns:        1,
	MaxIdleConnsPerHost: 1,
}

var client = &http.Client{
	Timeout:   10 * time.Second,
	Transport: netTransport,
}

// getSession returns a new sessionID used in further processing as a HTTP request header
func getSession() (string, error) {
	req, err := http.NewRequest("GET", SESSION_URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// Preventing mixed case field names
	body = bytes.ToLower(body)
	var s = new(Session)
	err = json.Unmarshal(body, s)
	if err != nil {
		return "", err
	}
	return s.SessionID, nil
}

// fetchID requests a data from single URL ID, and sends result to the results channel (defined as global)
func fetchID(id string, session string, parentID string, orderIndex int) {
	url := ID_URL + id

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		panic(err)
	}

	req.Header.Add("session", session)

	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	if resp.StatusCode != 200 {
		panic(fmt.Sprintf("bad response status code %v", resp.StatusCode))
	}
	// Preventing mixed case field names ("next", "NeXT", etc.)
	body = bytes.ToLower(body)

	var idResponse = new(CurbIDResponse)
	idResponse.Secret = "no"
	idResponse.ParentID = parentID
	idResponse.OrderIndex = orderIndex

	err = json.Unmarshal(body, &idResponse)
	if err != nil {
		panic(err)
	}
	resultsChan <- *idResponse
}

// readSecretsMap reads the resulting hashmap and prints secrets in the right order
func readSecretsMap(secretsMap map[string][]Secret, id string) {
	if secretList, ok := secretsMap[id]; ok == true {
		// sort node children base on the OrderIndex (the order in which nodes were visited):
		sort.Slice(secretList, func(i, j int) bool { return secretList[i].OrderIndex < secretList[j].OrderIndex })
		for _, secret := range secretList {
			if secret.Value != "" && secret.Value != "no" {
				fmt.Print(secret.Value)
			}
			readSecretsMap(secretsMap, secret.ID)
		}
	}
}

// crawl traverses the full tree of URLs and prints the results (secrets) at the end
func crawl(id string, session string) {
	rootID := "ROOT"
	secretsMap := map[string][]Secret{}
	go fetchID(id, session, rootID, 0)
	// 1 url is currently being fetched in background, loop while fetching
	for fetching := 1; fetching > 0; fetching-- {
		idResponse := <-resultsChan

		if idResponse.Message != "" && idResponse.Depth != 0 {
			panic(fmt.Sprintf("got unexpected message: %v\n", idResponse.Message))
		}
		// filling a hashmap that represent the tree of visited nodes for further reading at the end
		// a key in the map is a tree nodeID, values are node children
		value := secretsMap[idResponse.ParentID]
		secretsMap[idResponse.ParentID] = append(value, Secret{
			ID:         idResponse.ID,
			Value:      idResponse.Secret,
			OrderIndex: idResponse.OrderIndex,
		})
		// if secret is found then we're at the bottom of the URLs tree,
		// and there will be no "next" URLs to hop, so we just continue our loop
		if idResponse.Secret != "no" {
			continue
		}

		// if there is no secret fetched then we should have next URL IDs to hop
		for index, id := range idResponse.Next.IDs {
			// make sure that we don't send more requests than requestsChan buffer size
			requestsChan <- true
			// increasing number of fetching URLs for keeping our for... loop
			fetching++
			// "index" value will keep the order of URL visits,
			// which will help us to read the resulting data in a right order
			go func(id string, index int) {
				fetchID(id, session, idResponse.ID, index)
				<-requestsChan
			}(id, index)
		}
	}
	close(resultsChan)
	readSecretsMap(secretsMap, rootID)
}

func main() {
	session, err := getSession()
	if err != nil {
		fmt.Printf("Error: %v", err.Error())
		return
	}
	crawl("start", session)
}
