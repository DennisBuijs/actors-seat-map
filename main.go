package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Ticket struct {
	ID     int
	Holder string
	Status string
}

type UserActor struct {
	mailbox chan string
}

type ReservationActor struct {
	mailbox   chan string
	inventory *InventoryActor
	payment   *PaymentActor
}

type InventoryActor struct {
	mu      sync.Mutex
	tickets map[int]Ticket
}

type PaymentActor struct {
	mailbox  chan Ticket
	notifier *NotificationActor
}

type NotificationActor struct {
	mailbox chan Ticket
}

var inventory = NewInventoryActor()
var reservation = NewReservationActor(inventory, NewPaymentActor(NewNotificationActor()))
var user = NewUserActor()

func NewUserActor() *UserActor {
	actor := &UserActor{
		mailbox: make(chan string),
	}

	go func() {
		for UserID := range actor.mailbox {
			fmt.Printf("[UserActor] User %s requesting a ticket\n", UserID)
			reservation.mailbox <- UserID
		}
	}()

	return actor
}

func NewReservationActor(inventory *InventoryActor, payment *PaymentActor) *ReservationActor {
	actor := &ReservationActor{
		mailbox:   make(chan string),
		inventory: inventory,
		payment:   payment,
	}

	go func() {
		for userID := range actor.mailbox {
			ticket, success := inventory.ReserveTicket(userID)
			if success {
				fmt.Printf("[ReservationActor] User %s reserved ticket %d\n", userID, ticket.ID)
				payment.mailbox <- ticket
			} else {
				fmt.Printf("[ReservationActor] User %s failed to reserve a ticket\n", userID)
			}
		}
	}()

	return actor
}

func NewPaymentActor(notifier *NotificationActor) *PaymentActor {
	actor := &PaymentActor{
		mailbox:  make(chan Ticket),
		notifier: notifier,
	}

	go func() {
		for ticket := range actor.mailbox {
			time.Sleep(3 * time.Second)
			fmt.Printf("[PaymentActor] Payment received for ticket %d\n", ticket.ID)
			notifier.mailbox <- ticket
		}
	}()

	return actor
}

func NewInventoryActor() *InventoryActor {
	actor := &InventoryActor{
		tickets: make(map[int]Ticket),
	}

	for i := 1; i <= 100; i++ {
		actor.tickets[i] = Ticket{ID: i, Status: "Available"}
	}

	return actor
}

func (a *InventoryActor) ReserveTicket(userID string) (Ticket, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for id, ticket := range a.tickets {
		if ticket.Status == "Available" {
			ticket.Status = "Reserved"
			ticket.Holder = userID
			a.tickets[id] = ticket
			return ticket, true
		}
	}

	return Ticket{}, false
}

func NewNotificationActor() *NotificationActor {
	actor := &NotificationActor{
		mailbox: make(chan Ticket),
	}

	go func() {
		for ticket := range actor.mailbox {
			fmt.Printf("[NotificationActor] Ticket %d reserved by %s\n", ticket.ID, ticket.Holder)
		}
	}()

	return actor
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /reserve", ReserveHandler())

	http.ListenAndServe("localhost:3000", mux)
}

func ReserveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			http.Error(w, "User ID is required", http.StatusBadRequest)
			return
		}

		user.mailbox <- userID
		w.Write([]byte("{\"message\": \"Ticket request received\""))
	}
}
