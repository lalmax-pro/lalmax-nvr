package voip

import (
	"sync"
	"time"

	"github.com/q191201771/lalmax/voip"
)

type DialogState string

const (
	DialogStateEarly       DialogState = "early"       // INVITE sent, waiting for 200 OK
	DialogStateConfirmed   DialogState = "confirmed"   // 200 OK sent, waiting for ACK
	DialogStateEstablished DialogState = "established" // ACK received, call active
	DialogStateTerminated  DialogState = "terminated"  // BYE sent/received
)

type Dialog struct {
	CallID         string
	FromTag        string
	ToTag          string
	FromUser       string
	ToUser         string
	LocalCSeq      int
	RemoteCSeq     int
	InviteCSeq     int
	InviteBranch   string
	InviteURI      string
	Invite         *Message
	Peer           TransportAddr
	Held           bool
	State          DialogState
	PubSession     *voip.PubSession
	MediaCreatedAt time.Time
	AnsweredAt     time.Time
	CreatedAt      time.Time
	LastActivity   time.Time
	RemoteAddr     string
	OnTerminate    func()
	talk           *inboundTalk
}

type DialogManager struct {
	dialogs map[string]*Dialog // key: callID
	mutex   sync.RWMutex
}

func NewDialogManager() *DialogManager {
	return &DialogManager{
		dialogs: make(map[string]*Dialog),
	}
}

func (dm *DialogManager) Create(callID, fromTag, toTag, fromUser, toUser, remoteAddr string) *Dialog {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	dialog := &Dialog{
		CallID:       callID,
		FromTag:      fromTag,
		ToTag:        toTag,
		FromUser:     fromUser,
		ToUser:       toUser,
		State:        DialogStateEarly,
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		RemoteAddr:   remoteAddr,
	}

	dm.dialogs[callID] = dialog
	return dialog
}

func (dm *DialogManager) Get(callID string) *Dialog {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()
	if d := dm.dialogs[callID]; d != nil {
		copy := *d
		return &copy
	}
	return nil
}

// Take transfers ownership to the caller so disposal never holds the registry lock.
func (dm *DialogManager) Take(callID string) *Dialog {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	d := dm.dialogs[callID]
	delete(dm.dialogs, callID)
	return d
}
func (dm *DialogManager) Delete(callID string) {
	if d := dm.Take(callID); d != nil {
		if d.PubSession != nil {
			_ = d.PubSession.Dispose()
		}
		if d.OnTerminate != nil {
			d.OnTerminate()
		}
	}
}

func (dm *DialogManager) UpdateState(callID string, state DialogState) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	dialog := dm.dialogs[callID]
	if dialog != nil {
		dialog.State = state
		dialog.LastActivity = time.Now()
		if state == DialogStateEstablished && dialog.AnsweredAt.IsZero() {
			dialog.AnsweredAt = dialog.LastActivity
		}
	}
}

func (dm *DialogManager) UpdateActivity(callID string) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	dialog := dm.dialogs[callID]
	if dialog != nil {
		dialog.LastActivity = time.Now()
	}
}

func (dm *DialogManager) SetInvite(callID string, msg *Message, peer TransportAddr) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if d := dm.dialogs[callID]; d != nil {
		d.InviteCSeq = ExtractCSeqNumber(msg.CSeq())
		d.RemoteCSeq = d.InviteCSeq
		d.InviteBranch = msg.GetHeader("Via")
		d.InviteURI = msg.RequestURI
		invite := *msg
		invite.Headers = make(map[string][]string)
		for _, name := range []string{"Via", "From", "To", "Contact", "Record-Route", "Call-ID", "CSeq", "Content-Type"} {
			invite.Headers[name] = append([]string(nil), msg.GetHeaderAll(name)...)
		}
		if d.Invite != nil {
			invite.Headers["Record-Route"] = append([]string(nil), d.Invite.GetHeaderAll("Record-Route")...)
		}
		d.Invite, d.Peer = &invite, peer
		d.RemoteAddr = peer.String()
	}
}

func (dm *DialogManager) SetRemoteCSeq(callID string, seq int) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if d := dm.dialogs[callID]; d != nil {
		d.RemoteCSeq = seq
	}
}

func (dm *DialogManager) SetPubSession(callID string, session *voip.PubSession) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	dialog := dm.dialogs[callID]
	if dialog != nil {
		dialog.PubSession = session
	}
}

func (dm *DialogManager) SetTalk(callID string, talk *inboundTalk) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if d := dm.dialogs[callID]; d != nil {
		d.talk = talk
	}
}

func (dm *DialogManager) List() []*Dialog {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	list := make([]*Dialog, 0, len(dm.dialogs))
	for _, d := range dm.dialogs {
		copy := *d
		list = append(list, &copy)
	}
	return list
}

func (dm *DialogManager) SetMediaState(id string, epoch time.Time, held bool) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	if d := dm.dialogs[id]; d != nil {
		d.MediaCreatedAt = epoch
		d.Held = held
	}
}

func (dm *DialogManager) RefreshTarget(id string, msg *Message, peer TransportAddr) {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()
	d := dm.dialogs[id]
	if d == nil || d.Invite == nil {
		return
	}
	invite := *d.Invite
	invite.Headers = make(map[string][]string, len(d.Invite.Headers))
	for k, v := range d.Invite.Headers {
		invite.Headers[k] = append([]string(nil), v...)
	}
	if contact := msg.GetHeader("Contact"); contact != "" {
		invite.SetHeader("Contact", contact)
	}
	d.Invite = &invite
	d.Peer = peer
	d.RemoteAddr = peer.String()
}
