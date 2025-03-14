package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/r3labs/sse/v2"
)

type Ticket struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Status     string    `json:"status"`
	SeatID     string    `json:"seat_id"`
	EventID    string    `json:"event_id"`
	ReservedAt time.Time `json:"reserved_at"`
}

type Seat struct {
	ID     string `json:"id"`
	Row    string `json:"row"`
	Seat   int    `json:"seat"`
	Status string `json:"status"`
}

type Event struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Seats map[string]Seat `json:"seats"`
}

type ReservationRequest struct {
	UserID  string   `json:"user_id"`
	EventID string   `json:"event_id"`
	SeatIDs []string `json:"seat_ids"`
}

type UserActor struct {
	mailbox chan ReservationRequest
}

type ReservationActor struct {
	mailbox   chan ReservationRequest
	inventory *InventoryActor
	payment   *PaymentActor
}

type InventoryActor struct {
	mu      sync.Mutex
	tickets map[string]Ticket
	events  map[string]Event
	baskets map[string][]Ticket
}

type PaymentActor struct {
	mailbox  chan []Ticket
	notifier *NotificationActor
}

type NotificationActor struct {
	mailbox chan []Ticket
}

var inventory = NewInventoryActor()
var reservation = NewReservationActor(inventory, NewPaymentActor(NewNotificationActor()))
var user = NewUserActor()

var sseServer = sse.New()

var globalUserID = "01958247-10d3-7348-8006-c0d6db836a01" // until we have a real user implementation

func NewUserActor() *UserActor {
	actor := &UserActor{
		mailbox: make(chan ReservationRequest),
	}

	go func() {
		for req := range actor.mailbox {
			fmt.Printf("[UserActor] User %s requesting %d tickets\n", req.UserID, len(req.SeatIDs))
			reservation.mailbox <- req
		}
	}()

	return actor
}

func NewReservationActor(inventory *InventoryActor, payment *PaymentActor) *ReservationActor {
	actor := &ReservationActor{
		mailbox:   make(chan ReservationRequest),
		inventory: inventory,
		payment:   payment,
	}

	go func() {
		for req := range actor.mailbox {
			tickets, success := inventory.ReserveTicket(req.UserID, req.EventID, req.SeatIDs)
			if success {
				fmt.Printf("[ReservationActor] User %s reserved %d tickets for event %s\n", req.UserID, len(req.SeatIDs), req.EventID)
				sseServer.Publish(req.UserID, &sse.Event{
					Event: []byte("reservation"),
					Data:  []byte(inventory.GetBasketAsHTML(req.UserID)),
				})

				for _, seatID := range req.SeatIDs {
					sseServer.Publish(req.EventID, &sse.Event{
						Event: []byte(seatID),
						Data:  []byte(inventory.GetSeatAsHTML(req.EventID, seatID)),
					})
				}
				payment.mailbox <- tickets
			} else {
				fmt.Printf("[ReservationActor] User %s failed to reserve a ticket\n", req.UserID)
			}
		}
	}()

	return actor
}

func NewPaymentActor(notifier *NotificationActor) *PaymentActor {
	actor := &PaymentActor{
		mailbox:  make(chan []Ticket),
		notifier: notifier,
	}

	go func() {
		for tickets := range actor.mailbox {
			fmt.Printf("[PaymentActor] Payment received for %d tickets\n", len(tickets))
			notifier.mailbox <- tickets
		}
	}()

	return actor
}

func NewInventoryActor() *InventoryActor {
	actor := &InventoryActor{
		tickets: make(map[string]Ticket),
		events:  make(map[string]Event),
		baskets: make(map[string][]Ticket),
	}

	event := Event{
		ID:    "event_1",
		Name:  "Event 1",
		Seats: make(map[string]Seat),
	}

	event.Seats = make(map[string]Seat, 50)
	rows := []string{"A", "B", "C", "D", "E"}
	for _, row := range rows {
		for i := range 10 {
			seatNumStr := fmt.Sprintf("%02d", i+1)

			seat := Seat{
				ID:     "seat-" + row + "-" + seatNumStr,
				Row:    row,
				Seat:   i + 1,
				Status: "Available",
			}
			event.Seats[seat.ID] = seat
		}
	}

	actor.events[event.ID] = event
	sseServer.CreateStream(event.ID)

	return actor
}

func (a *InventoryActor) ReserveTicket(userID string, eventID string, seatIDs []string) ([]Ticket, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	event, exists := a.events[eventID]
	if !exists {
		return nil, false
	}

	for _, seatID := range seatIDs {
		if seat, ok := event.Seats[seatID]; !ok || seat.Status != "Available" {
			return nil, false
		}
	}

	reservedTickets := make([]Ticket, 0, len(seatIDs))
	reservationTime := time.Now()

	for _, seatID := range seatIDs {
		seat := event.Seats[seatID]
		seat.Status = "Reserved"
		event.Seats[seatID] = seat

		ticket := Ticket{
			ID:         seat.ID,
			UserID:     userID,
			EventID:    eventID,
			SeatID:     seatID,
			ReservedAt: reservationTime,
		}

		a.tickets[ticket.ID] = ticket
		reservedTickets = append(reservedTickets, ticket)
	}

	a.events[eventID] = event
	a.baskets[userID] = append(a.baskets[userID], reservedTickets...)

	return reservedTickets, true
}

func NewNotificationActor() *NotificationActor {
	actor := &NotificationActor{
		mailbox: make(chan []Ticket),
	}

	go func() {
		for tickets := range actor.mailbox {
			fmt.Printf("[NotificationActor] %d tickets reserved by %s\n", len(tickets), tickets[0].UserID)
		}
	}()

	return actor
}

func main() {
	sseServer.CreateStream(globalUserID)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /event/{eventID}", EventShowHandler())
	mux.HandleFunc("POST /reserve", ReserveHandler())

	mux.HandleFunc("GET /sse", sseServer.ServeHTTP)

	http.ListenAndServe("localhost:3000", mux)
}

func ReserveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ReservationRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		user.mailbox <- req

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{\"message\": \"Ticket request received\"}"))
	}
}

func EventShowHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventID := r.PathValue("eventID")
		event := inventory.GetEvent(eventID)

		tmpl := template.Must(template.ParseFiles("templates/base.html", "templates/event.html"))
		err := tmpl.Execute(w, event)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (a *InventoryActor) GetEvent(eventID string) Event {
	return a.events[strings.ToLower(eventID)]
}

func (t Ticket) Seat() string {
	event := inventory.GetEvent(t.EventID)
	seat := event.Seats[t.SeatID]
	return fmt.Sprintf("%s-%d", seat.Row, seat.Seat)
}

func (a *InventoryActor) GetBasketAsHTML(userID string) string {
	basket := a.baskets[userID]
	var html string
	for _, ticket := range basket {
		html += fmt.Sprintf("<li>%s</li>", ticket.Seat())
	}
	return html
}

func (a *InventoryActor) GetSeatAsHTML(eventID string, seatID string) string {
	seat := a.events[eventID].Seats[seatID]
	return fmt.Sprintf("<button class=\"%s\" data-seat-id=\"%s\" sse-swap=\"%s\">%d</button>", seat.Status, seat.ID, seat.ID, seat.Seat)
}
