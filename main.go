//go:build !js
// +build !js

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

var (
	api      *webrtc.API
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

func doSignaling(w http.ResponseWriter, r *http.Request) error {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}

	if _, err := peerConnection.CreateDataChannel("noop", nil); err != nil {
		return err
	}

	writeLocalDescription := func(event string) error {
		response, err := json.Marshal(*peerConnection.LocalDescription())
		if err != nil {
			return err
		}

		return ws.WriteJSON(&websocketMessage{
			Event: event,
			Data:  string(response),
		})
	}

	peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
		if i == nil {
			return
		}

		candidateString, err := json.Marshal(i.ToJSON())
		if err != nil {
			log.Println(err)
			return
		}

		if writeErr := ws.WriteJSON(&websocketMessage{
			Event: "candidate",
			Data:  string(candidateString),
		}); writeErr != nil {
			log.Println(writeErr)
		}
	})

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		d.OnOpen(func() {
			for range time.Tick(time.Second * 3) {
				if err = d.SendText(time.Now().String()); err != nil {
					if errors.Is(io.ErrClosedPipe, err) {
						return
					}
					log.Print(err)
				}
			}
		})
	})

	message := &websocketMessage{}
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return err
		} else if err := json.Unmarshal(raw, &message); err != nil {
			return err
		}

		switch message.Event {
		case "candidate":
		case "requestOffer":
			// Create channel that is blocked until ICE Gathering is complete
			gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

			offer, err := peerConnection.CreateOffer(nil)
			if err != nil {
				return err
			} else if err = peerConnection.SetLocalDescription(offer); err != nil {
				return err
			}

			// Block until ICE Gathering is complete, disabling trickle ICE
			// we do this because we only can exchange one signaling message
			// in a production application you should exchange ICE Candidates via OnICECandidate
			<-gatherComplete

			if err := writeLocalDescription("offer"); err != nil {
				return err
			}
		case "offer", "answer":
			sessionDescription := webrtc.SessionDescription{}
			if err := json.Unmarshal([]byte(message.Data), &sessionDescription); err != nil {
				return err
			} else if err := peerConnection.SetRemoteDescription(sessionDescription); err != nil {
				return err
			} else if message.Event == "offer" {
				// Create channel that is blocked until ICE Gathering is complete
				gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

				answer, err := peerConnection.CreateAnswer(nil)
				if err != nil {
					return err
				} else if err = peerConnection.SetLocalDescription(answer); err != nil {
					return err
				}

				// Block until ICE Gathering is complete, disabling trickle ICE
				// we do this because we only can exchange one signaling message
				// in a production application you should exchange ICE Candidates via OnICECandidate
				<-gatherComplete

				if err := writeLocalDescription("answer"); err != nil {
					return err
				}
			}
		}
	}
}

func main() {
	settingEngine := webrtc.SettingEngine{}

	tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{
		IP:   net.IP{0, 0, 0, 0},
		Port: 8443,
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("Listening for ICE TCP at %s\n", tcpListener.Addr())

	tcpMux := webrtc.NewICETCPMux(nil, tcpListener, 8)
	settingEngine.SetICETCPMux(tcpMux)

	if len(os.Args) == 2 {
		fmt.Printf("Setting Listening IP %s\n", os.Args[1])
		settingEngine.SetNAT1To1IPs([]string{os.Args[1]}, webrtc.ICECandidateTypeHost)
	}

	api = webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

	http.Handle("/", http.FileServer(http.Dir(".")))
	http.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		if err := doSignaling(w, r); err != nil {
			log.Print(err)
		}
	})

	fmt.Println("Open http://localhost:8080 to access this demo")
	panic(http.ListenAndServe(":8080", nil))
}
