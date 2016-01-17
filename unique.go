package queue

import (
	"sync"

	log "gopkg.in/Sirupsen/logrus.v0"
)

const (
	// DefaultQueueLength is the length (size) of `In` and `Out` queues (channels)
	DefaultQueueLength = 100
	// DefaultBufferSize is the size of the internal buffer from In --> Out
	DefaultBufferSize  = 100
)

// InQueue is the type for `In` queues
type InQueue chan interface{}
// OutQueue is the type for `Out` queue
type OutQueue chan interface{}

// UniqueQueue is the containing type for set-style / unique queues
type UniqueQueue struct {
	// MatcherID is a callback function to get an ID that can be used to identify whether an
	// incoming item is already in the queue
	// No default, you *must* provide a callback, e.g:
	// MatcherID: func(val interface{}) string {
	// 	return val.String()
	// }
	MatcherID   func(interface{}) string
	// In is the "Incoming Items" queue (channel)
	In          InQueue
	// Out is the "Outgoing Items" queue (channel)
	Out         OutQueue
	// QueueLength is Size for `In` and `Out` queues, when `Init()` creates them
	// Defaults to `DefaultQueueLength`
	QueueLength int
	// BufferSize is the size for the internal buffer
	// You usually want to make this large enough that your `In` queue doesn't block (much) and your
	// `Out` queue is well-hydrated
	// Defaults to `DefaultBufferSize`
	BufferSize  int
	// Set of URLs, ordered by incoming
	uniqueIDs   map[string]bool
	// Internal buffer for determining when to pop an item off the `uniqueIDs` set
	feeder      chan interface{}
	feederOk    bool
	wg          sync.WaitGroup
}

// Initialiser for a UniqueQueue struct-literal
// Applies any Defaults
// You must provide a MatcherID callback, all other values have 
func (q *UniqueQueue) Init() *UniqueQueue {
	// TODO: Implement this, probably using reflection (?):
	// if q.MatcherID == nil {
	// 	q.MatcherID = func(val interface{}) string {
	// 		return val.String()
	// 	}
	// }

	if q.QueueLength == 0 {
		q.QueueLength = DefaultQueueLength
	}
	if q.BufferSize == 0 {
		q.BufferSize = DefaultBufferSize
	}
	if q.In == nil {
		q.In = make(chan interface{}, q.QueueLength)
	}
	if q.Out == nil {
		q.Out = make(chan interface{}, q.QueueLength)
	}

	q.feederOk = true
	q.uniqueIDs = make(map[string]bool)
	// The queue length should probably be configurable:
	q.feeder = make(chan interface{}, q.BufferSize)

	return q
}

// Run starts the background FIFO from `In` --> `Out`
func (q *UniqueQueue) Run() {
	if cap(q.Out) == 0 {
		panic("Outgoing queue needs to be > 0 to avoid deadlocks")
	}
	q.wg.Add(1)
	go q.fifo()
}

// Close arranges for the background FIFO to shut down
func (q *UniqueQueue) Close() {
	q.feederOk = false
	q.wg.Wait()
}

// Push the data from `In` queue to `Out` queue, using an internal FIFO buffer.
// Any incoming items that are already in the FIFO are dropped.
func (q *UniqueQueue) fifo() {
	defer q.wg.Done()
	// If `In` is closed, there's nothing incoming:
	defer close(q.feeder)

	inQueueOk := true
	counter := 0

	for q.feederOk && (inQueueOk || len(q.feeder) > 0) {
		select {
		case newItem, inQueueOk := <-q.In:
			if inQueueOk {
				counter++
				log.WithFields(log.Fields{
					"Count": counter,
				}).Debug("Incoming item")
				q.pushUnique(newItem)
			}

		default:
			// We don't want this to block the rest of the loop
		}

		// If the `Out` has room:
		if len(q.Out) < cap(q.Out) {
			q.popTo(func(newItem interface{}) {
				q.Out <- newItem
			})
		}
	}
}

func (q *UniqueQueue) getID(item interface{}) string {
	// I'd rather give meaningful failures
	if q.MatcherID == nil {
		panic("MatcherID func() not provided")
	}

	return q.MatcherID(item)
}

func (q *UniqueQueue) pushUnique(item interface{}) bool {
	id := q.getID(item)

	log.WithFields(log.Fields{
		"ID":   id,
		"Item": item,
	}).Debug("Incoming Item")

	// We don't have this item queued
	if q.uniqueIDs[id] {
		log.WithFields(log.Fields{
			"ID":   id,
			"item": item,
		}).Debug("Item is already queued")
		return false
	}

	q.feeder <- item
	q.uniqueIDs[id] = true

	log.WithFields(log.Fields{
		"feeder":    len(q.feeder),
		"uniqueIDs": len(q.uniqueIDs),
	}).Debug("Incoming Queue Lengths")

	return true
}

func (q *UniqueQueue) popTo(sendTo func(interface{})) {
	select {
	case item, feederOk := <-q.feeder:
		if !feederOk {
			log.Debug("Feeder Queue is closed")
			q.feederOk = false
			return
		}

		id := q.getID(item)

		log.WithFields(log.Fields{
			"ID":   id,
			"Item": item,
		}).Debug("Outgoing Item")

		sendTo(item)
		delete(q.uniqueIDs, id) // drop it from the set

		log.WithFields(log.Fields{
			"feeder":    len(q.feeder),
			"uniqueIDs": len(q.uniqueIDs),
		}).Debug("Outgoing Queue Lengths")

	default:
		// We don't want this to block the rest of the loop
	}
}
