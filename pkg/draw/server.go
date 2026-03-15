// Package draw provides a tiny WebSocket-based collaborative whiteboard server
package draw

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

const maxTextLen = 65536
const wsTimeout = 60 * time.Second
const pingPeriod = wsTimeout * 9 / 10

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,

	// Allow connections from any domain (needed for deployment)
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Server represents a DoodleVerse server
type Server struct {
	Room       *Room
	BotClient  *Client
	loginCodes map[string]User
}

func (srv *Server) connect(rm *Room, w http.ResponseWriter, r *http.Request) {
	connlock := sync.Mutex{}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	var client *Client

	// Keep connection alive with ping
	go func() {
		for {
			time.Sleep(pingPeriod)

			connlock.Lock()
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				if client != nil {
					client.Leave()
				}
				connlock.Unlock()
				return
			}
			connlock.Unlock()
		}
	}()

	messageLimiter := rate.NewLimiter(10, 1)

	for {
		var msg Message

		err := conn.ReadJSON(&msg)
		connlock.Lock()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)

			if client != nil {
				client.Leave()
			}

			connlock.Unlock()
			break
		}
		connlock.Unlock()

		switch msg.Type {

		case msgHello:

			parts := strings.Split(msg.Text, "\n")
			if len(parts) != 2 {
				break
			}

			u := User{
				Name:  parts[0],
				Color: parts[1],
			}

			client = srv.Room.Enter(u)

			client.OnMessage = func(msg Message) {
				connlock.Lock()
				conn.WriteJSON(msg)
				connlock.Unlock()
			}

			client.Send(msgHello, msg.Text)
			client.BroadcastUserList()

			log.Println("User joined:", u.Name)

		case msgText:

			if client == nil || !messageLimiter.Allow() {
				break
			}

			if len(msg.Text) > maxTextLen {
				msg.Text = msg.Text[:maxTextLen]
			}

			client.Send(msgText, msg.Text)

		case msgEmptyCanvas:

			if client == nil || !messageLimiter.Allow() {
				break
			}

			client.Send(msgEmptyCanvas, "")

		case msgChangeUser:

			parts := strings.Split(msg.Text, "\n")
			if len(parts) != 2 {
				break
			}

			if client == nil || !messageLimiter.Allow() {
				break
			}

			client.Send(msgChangeUser, msg.Text)

			client.User.Name = parts[0]
			client.User.Color = parts[1]

		default:
			log.Printf("Unknown message type: %v", msg)
		}
	}
}

// Serve the frontend
func handleHome(w http.ResponseWriter, r *http.Request) {

	indexFile, err := os.Open("./static/index.html")
	if err != nil {
		http.Error(w, "Could not load page", 500)
		return
	}
	defer indexFile.Close()

	io.Copy(w, indexFile)
}

// StartServer launches the DoodleVerse server
func StartServer() {

	r := mux.NewRouter()

	drawSrv := Server{
		Room:       NewRoom(),
		loginCodes: make(map[string]User),
	}

	r.HandleFunc("/", handleHome)
	r.PathPrefix("/static/").Handler(
		http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))),
	)

	r.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		drawSrv.connect(drawSrv.Room, w, r)
	})

	// Dynamic port for cloud deployment
	port := os.Getenv("PORT")
	if port == "" {
		port = "1243"
	}

	srv := &http.Server{
		Handler:      r,
		Addr:         ":" + port,
		WriteTimeout: wsTimeout,
		ReadTimeout:  wsTimeout,
	}

	log.Printf("DoodleVerse server running on port %s\n", port)
	log.Fatal(srv.ListenAndServe())
}