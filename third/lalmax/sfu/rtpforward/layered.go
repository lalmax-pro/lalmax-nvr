package rtpforward

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/q191201771/lalmax/sfu/layerselect"
)

var (
	ErrInvalidLayer          = errors.New("rtp fanout: invalid layer")
	ErrLayerNotFound         = errors.New("rtp fanout: layer not found")
	ErrNoLayers              = errors.New("rtp fanout: no layers available")
	ErrLayerSubscriberClosed = errors.New("rtp fanout: layered subscriber closed")
)

// LayerInfo describes one source encoding. MaxBitrate is the sender's
// configured encoding cap and is used as a conservative selection estimate.
type LayerInfo struct {
	RID        string
	MaxBitrate uint64
}

type layeredSource struct {
	info   LayerInfo
	fanout *Fanout
}

// LayeredFanout fans out a single publication with independently selectable
// RTP encodings. Each downstream subscriber receives only its selected RID.
type LayeredFanout struct {
	mu             sync.RWMutex
	closed         bool
	maxSubscribers int
	queueSize      int
	layers         map[string]*layeredSource
	subscribers    map[string]*LayeredSubscriber
}

func NewLayered(maxSubscribers, queueSize int) (*LayeredFanout, error) {
	if maxSubscribers < 1 || maxSubscribers > 1024 || queueSize < 1 || queueSize > 256 {
		return nil, ErrInvalidConfig
	}
	return &LayeredFanout{
		maxSubscribers: maxSubscribers,
		queueSize:      queueSize,
		layers:         make(map[string]*layeredSource),
		subscribers:    make(map[string]*LayeredSubscriber),
	}, nil
}

func validRID(rid string) bool {
	if len(rid) > 16 {
		return false
	}
	if rid == "" {
		return true
	}
	for _, r := range rid {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (f *LayeredFanout) AddLayer(info LayerInfo) error {
	if !validRID(info.RID) || (info.RID != "" && info.MaxBitrate == 0) || info.MaxBitrate > 100_000_000 {
		return ErrInvalidLayer
	}
	queue, err := New(f.maxSubscribers, f.queueSize)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		queue.Close()
		return ErrClosed
	}
	if _, exists := f.layers[info.RID]; exists {
		f.mu.Unlock()
		queue.Close()
		return ErrInvalidLayer
	}
	f.layers[info.RID] = &layeredSource{info: info, fanout: queue}
	f.mu.Unlock()
	f.reevaluateSubscribers()
	return nil
}

func (f *LayeredFanout) RemoveLayer(rid string) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return ErrClosed
	}
	layer := f.layers[rid]
	if layer == nil {
		f.mu.Unlock()
		return ErrLayerNotFound
	}
	delete(f.layers, rid)
	f.mu.Unlock()
	layer.fanout.Close()
	f.reevaluateSubscribers()
	return nil
}

func (f *LayeredFanout) reevaluateSubscribers() {
	f.mu.RLock()
	subs := make([]*LayeredSubscriber, 0, len(f.subscribers))
	for _, sub := range f.subscribers {
		subs = append(subs, sub)
	}
	f.mu.RUnlock()
	for _, sub := range subs {
		sub.mu.Lock()
		target := sub.target
		closed := sub.closed
		sub.mu.Unlock()
		if !closed {
			_ = sub.SetTargetBitrate(target)
		}
	}
}

func (f *LayeredFanout) Layers() []LayerInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	layers := make([]LayerInfo, 0, len(f.layers))
	for _, layer := range f.layers {
		layers = append(layers, layer.info)
	}
	sort.Slice(layers, func(i, j int) bool {
		if layers[i].MaxBitrate == layers[j].MaxBitrate {
			return layers[i].RID < layers[j].RID
		}
		return layers[i].MaxBitrate < layers[j].MaxBitrate
	})
	return layers
}

func (f *LayeredFanout) WriteRTP(rid string, packet *rtp.Packet) error {
	f.mu.RLock()
	if f.closed {
		f.mu.RUnlock()
		return ErrClosed
	}
	layer := f.layers[rid]
	f.mu.RUnlock()
	if layer == nil {
		return ErrLayerNotFound
	}
	return layer.fanout.WriteRTP(packet)
}

func (f *LayeredFanout) Subscribe(id string) (*LayeredSubscriber, error) {
	layers := f.Layers()
	if len(layers) == 0 {
		return nil, ErrNoLayers
	}
	return f.SubscribeLayer(id, layers[0].RID)
}

func (f *LayeredFanout) SubscribeLayer(id, rid string) (*LayeredSubscriber, error) {
	if id == "" {
		return nil, ErrInvalidConfig
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrClosed
	}
	if len(f.layers) == 0 {
		return nil, ErrNoLayers
	}
	if _, exists := f.subscribers[id]; exists {
		return nil, ErrDuplicateSubscriber
	}
	if len(f.subscribers) >= f.maxSubscribers {
		return nil, ErrSubscriberLimit
	}
	layer := f.layers[rid]
	if layer == nil {
		return nil, ErrLayerNotFound
	}
	input, err := layer.fanout.Subscribe(id)
	if err != nil {
		return nil, err
	}
	sub := &LayeredSubscriber{
		owner:      f,
		id:         id,
		currentRID: rid,
		input:      input,
		packets:    make(chan *rtp.Packet, f.queueSize),
		requests:   make(chan layerRequest),
		done:       make(chan struct{}),
		finished:   make(chan struct{}),
	}
	f.subscribers[id] = sub
	go sub.run()
	return sub, nil
}

func (f *LayeredFanout) subscribeLayer(id, rid string) (*Subscriber, error) {
	f.mu.RLock()
	if f.closed {
		f.mu.RUnlock()
		return nil, ErrClosed
	}
	layer := f.layers[rid]
	f.mu.RUnlock()
	if layer == nil {
		return nil, ErrLayerNotFound
	}
	return layer.fanout.Subscribe(id)
}

func (f *LayeredFanout) drop(sub *LayeredSubscriber) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subscribers[sub.id] == sub {
		delete(f.subscribers, sub.id)
	}
}

func (f *LayeredFanout) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	layers := make([]*layeredSource, 0, len(f.layers))
	for _, layer := range f.layers {
		layers = append(layers, layer)
	}
	subs := make([]*LayeredSubscriber, 0, len(f.subscribers))
	for _, sub := range f.subscribers {
		subs = append(subs, sub)
	}
	clear(f.layers)
	clear(f.subscribers)
	f.mu.Unlock()
	for _, layer := range layers {
		layer.fanout.Close()
	}
	for _, sub := range subs {
		sub.closeWithReason(ErrClosed)
	}
}

type layerRequest struct {
	rid string
	ack chan error
}

// LayeredSubscriber keeps one stable packet channel while its selected source
// RID changes. Sequence numbers are rebased at switches; timestamps remain the
// source's shared capture clock across simulcast encodings.
type LayeredSubscriber struct {
	owner *LayeredFanout
	id    string

	mu            sync.Mutex
	closed        bool
	reason        error
	stopReason    error
	currentRID    string
	lastRID       string
	lastSeq       uint16
	seqOffset     uint16
	seqReady      bool
	target        uint64
	selector      layerselect.Selector
	onLayerChange func(string)
	packets       chan *rtp.Packet
	input         *Subscriber
	requests      chan layerRequest
	done          chan struct{}
	finished      chan struct{}
	stopOnce      sync.Once
	finishOnce    sync.Once
	switchMu      sync.Mutex
}

func (s *LayeredSubscriber) Packets() <-chan *rtp.Packet { return s.packets }

func (s *LayeredSubscriber) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

func (s *LayeredSubscriber) SelectedLayer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentRID
}

// SetOnLayerChange installs a callback invoked after an encoding switch.
// It is intended for transport feedback such as requesting a keyframe.
func (s *LayeredSubscriber) SetOnLayerChange(callback func(string)) {
	s.mu.Lock()
	s.onLayerChange = callback
	s.mu.Unlock()
}

func (s *LayeredSubscriber) SelectLayer(rid string) error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	s.mu.Lock()
	if s.closed {
		err := s.reason
		if err == nil {
			err = ErrLayerSubscriberClosed
		}
		s.mu.Unlock()
		return err
	}
	if s.currentRID == rid && s.input != nil {
		s.mu.Unlock()
		return nil
	}
	if s.currentRID == rid && s.input == nil {
		// The selected encoding ended between control updates. Reopening the
		// same RID will fail until a new track is published.
	}
	s.mu.Unlock()
	ack := make(chan error, 1)
	select {
	case s.requests <- layerRequest{rid: rid, ack: ack}:
	case <-s.done:
		return ErrLayerSubscriberClosed
	}
	select {
	case err := <-ack:
		return err
	case <-s.done:
		return ErrLayerSubscriberClosed
	}
}

func (s *LayeredSubscriber) SetTargetBitrate(target uint64) error {
	layers := s.owner.Layers()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrLayerSubscriberClosed
	}
	current := s.currentRID
	s.target = target
	next := s.selector.Choose(current, target, asPolicyLayers(layers), time.Now())
	s.mu.Unlock()
	return s.SelectLayer(next)
}

func asPolicyLayers(layers []LayerInfo) []layerselect.Layer {
	converted := make([]layerselect.Layer, 0, len(layers))
	for _, layer := range layers {
		converted = append(converted, layerselect.Layer{RID: layer.RID, MaxBitrate: layer.MaxBitrate})
	}
	return converted
}

func (s *LayeredSubscriber) Close() {
	s.closeWithReason(ErrSubscriberClosed)
}

func (s *LayeredSubscriber) closeWithReason(reason error) {
	s.mu.Lock()
	if !s.closed {
		s.stopReason = reason
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.done) })
	<-s.finished
}

func (s *LayeredSubscriber) run() {
	input := s.input
	var reason error
	defer func() {
		if input != nil {
			input.Close()
		}
		s.finish(reason)
	}()
	for {
		var inputPackets <-chan *rtp.Packet
		if input != nil {
			inputPackets = input.Packets()
		}
		select {
		case <-s.done:
			s.mu.Lock()
			reason = s.stopReason
			s.mu.Unlock()
			if reason == nil {
				reason = ErrSubscriberClosed
			}
			return
		case packet, open := <-inputPackets:
			if !open {
				if input != nil && errors.Is(input.Err(), ErrClosed) {
					s.owner.mu.RLock()
					ownerClosed := s.owner.closed
					s.owner.mu.RUnlock()
					if ownerClosed {
						reason = ErrClosed
						return
					}
					input = nil
					s.mu.Lock()
					s.input = nil
					s.mu.Unlock()
					continue
				}
				reason = input.Err()
				return
			}
			output := s.rewriteSequence(packet)
			select {
			case s.packets <- output:
			default:
				reason = ErrSlowSubscriber
				return
			}
		case request := <-s.requests:
			if request.rid == s.SelectedLayer() && input != nil {
				request.ack <- nil
				continue
			}
			next, err := s.owner.subscribeLayer(s.id, request.rid)
			var onLayerChange func(string)
			if err == nil {
				old := input
				input = next
				s.mu.Lock()
				changed := s.currentRID != request.rid
				s.input = next
				s.currentRID = request.rid
				if changed {
					onLayerChange = s.onLayerChange
				}
				s.mu.Unlock()
				if old != nil {
					old.Close()
				}
				if changed {
					s.dropBufferedPackets()
				}
			}
			request.ack <- err
			if err == nil && onLayerChange != nil {
				onLayerChange(request.rid)
			}
		}
	}
}

// dropBufferedPackets removes RTP queued for the previous RID before a layer
// switch is acknowledged. The input reader and switch requests share one run
// goroutine, so no old-RID packet can be enqueued after this drain. Account for
// the discarded packets when rebasing sequence numbers at the next RID.
func (s *LayeredSubscriber) dropBufferedPackets() {
	dropped := 0
	for {
		select {
		case <-s.packets:
			dropped++
		default:
			if dropped > 0 {
				s.mu.Lock()
				s.lastSeq -= uint16(dropped)
				s.mu.Unlock()
			}
			return
		}
	}
}

func (s *LayeredSubscriber) rewriteSequence(packet *rtp.Packet) *rtp.Packet {
	output := packet.Clone()
	rid := s.SelectedLayer()
	s.mu.Lock()
	if !s.seqReady || s.lastRID != rid {
		if s.seqReady {
			s.seqOffset = s.lastSeq + 1 - packet.SequenceNumber
		}
		s.lastRID = rid
		s.seqReady = true
	}
	output.SequenceNumber = packet.SequenceNumber + s.seqOffset
	s.lastSeq = output.SequenceNumber
	s.mu.Unlock()
	return output
}

func (s *LayeredSubscriber) finish(reason error) {
	s.finishOnce.Do(func() {
		s.stopOnce.Do(func() { close(s.done) })
		s.mu.Lock()
		s.closed = true
		s.reason = reason
		close(s.packets)
		s.mu.Unlock()
		s.owner.drop(s)
		close(s.finished)
	})
}
