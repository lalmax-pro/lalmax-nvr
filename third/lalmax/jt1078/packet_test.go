package jt1078

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestDecodeAtomicIFrame(t *testing.T) {
	raw, err := hex.DecodeString("3031636481060000295696659617010000000000000000000000000000020000")
	if err != nil {
		t.Fatal(err)
	}
	pkt, remain, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(remain) != 0 {
		t.Fatalf("remain=%x", remain)
	}
	if pkt.Sim != "295696659617" {
		t.Fatalf("sim=%s", pkt.Sim)
	}
	if pkt.LogicChannel != 1 {
		t.Fatalf("channel=%d", pkt.LogicChannel)
	}
	if pkt.DataType != DataTypeI {
		t.Fatalf("dataType=%s", pkt.DataType)
	}
	if pkt.SubcontractType != SubcontractTypeAtomic {
		t.Fatalf("sub=%s", pkt.SubcontractType)
	}
	if pkt.StreamName() != "295696659617_1" {
		t.Fatalf("stream=%s", pkt.StreamName())
	}
	if pkt.PhoneLen != simBCDLen || !pkt.PhoneLenConfident {
		t.Fatalf("phone=%d confident=%v", pkt.PhoneLen, pkt.PhoneLenConfident)
	}
}

func TestDecodeUnqualified(t *testing.T) {
	raw, _ := hex.DecodeString("2031636481e20000295696659617010000000000000000000000000000020000")
	_, _, err := Decode(raw)
	if !errors.Is(err, ErrUnqualifiedData) {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeHeaderTooShort(t *testing.T) {
	raw, _ := hex.DecodeString("3031636481e200")
	_, _, err := Decode(raw)
	if !errors.Is(err, ErrHeaderLengthShort) {
		t.Fatalf("err=%v", err)
	}
}

func TestDecodeBodyTooShort(t *testing.T) {
	raw, _ := hex.DecodeString("3031636481e200002956966596170100000000000000000000000000000200")
	_, remain, err := Decode(raw)
	if !errors.Is(err, ErrBodyLengthShort) {
		t.Fatalf("err=%v remain=%x", err, remain)
	}
}

func TestDecodeRemain(t *testing.T) {
	raw, _ := hex.DecodeString("3031636481e200002956966596170100000000000000000000000000000200003031636481e20000295696659617010000000000000000000000000000020000")
	_, remain, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("3031636481e20000295696659617010000000000000000000000000000020000")
	if !bytes.Equal(remain, want) {
		t.Fatalf("remain=%x", remain)
	}
}

func TestDecodePenetrate(t *testing.T) {
	raw, _ := hex.DecodeString("3031636481e200002956966596170240000101")
	pkt, remain, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(remain) != 0 {
		t.Fatalf("remain=%x", remain)
	}
	if pkt.DataType != DataTypePenetrate {
		t.Fatalf("dataType=%s", pkt.DataType)
	}
	if pkt.LogicChannel != 2 {
		t.Fatalf("channel=%d", pkt.LogicChannel)
	}
	if !bytes.Equal(pkt.Body, []byte{0x01}) {
		t.Fatalf("body=%x", pkt.Body)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	pkt := Packet{
		ID: magicID,
		Flag: Flag{
			V:  2,
			CC: 1,
			M:  1,
			PT: PTH264,
		},
		Sim:          "295696659617",
		LogicChannel: 1,
		DataType:     DataTypeI,
		Body:         []byte{0x00, 0x00},
	}
	raw, err := pkt.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := hex.DecodeString("3031636481e20000295696659617010000000000000000000000000000020000")
	if !bytes.Equal(raw, want) {
		t.Fatalf("got=%x want=%x", raw, want)
	}
	got, remain, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(remain) != 0 {
		t.Fatalf("remain=%x", remain)
	}
	if got.Sim != pkt.Sim || got.LogicChannel != pkt.LogicChannel || got.Flag.PT != PTH264 {
		t.Fatalf("decoded=%+v", got)
	}
}

func TestEncodeG711A(t *testing.T) {
	body := bytes.Repeat([]byte{0xd5}, 800)
	pkt := Packet{
		Flag: Flag{
			V:  2,
			CC: 1,
			PT: PTG711A,
		},
		Seq:          300,
		Sim:          "1003",
		LogicChannel: 1,
		DataType:     DataTypeA,
		Timestamp:    1746418625361,
		Body:         body,
	}
	raw, err := pkt.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, remain, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(remain) != 0 {
		t.Fatalf("remain=%x", remain)
	}
	if got.Sim != "1003" || got.Flag.PT != PTG711A || got.Seq != 300 {
		t.Fatalf("decoded=%+v", got)
	}
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("body mismatch")
	}
}

func TestBCD(t *testing.T) {
	if got := bcdToString(stringToBCD("1003", 6)); got != "1003" {
		t.Fatalf("got=%s", got)
	}
	if got := bcdToString(stringToBCD("295696659617", 6)); got != "295696659617" {
		t.Fatalf("got=%s", got)
	}
	if got := bcdToString([]byte{0, 0, 0, 0, 0, 0}); got != "0" {
		t.Fatalf("got=%s", got)
	}
}

func TestDecode2019Phone(t *testing.T) {
	pkt := Packet{
		PhoneLen: simBCDLen2019,
		Flag: Flag{
			V:  2,
			CC: 1,
			M:  1,
			PT: PTG711A,
		},
		Sim:             "13800138000",
		LogicChannel:    1,
		DataType:        DataTypeA,
		SubcontractType: SubcontractTypeAtomic,
		Timestamp:       100,
		Body:            []byte{0xd5},
	}
	raw, err := pkt.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, remain, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(remain) != 0 {
		t.Fatalf("remain=%x", remain)
	}
	if got.PhoneLen != simBCDLen2019 || got.Sim != "13800138000" || got.LogicChannel != 1 {
		t.Fatalf("decoded phone=%d sim=%s ch=%d", got.PhoneLen, got.Sim, got.LogicChannel)
	}
	if !bytes.Equal(got.Body, []byte{0xd5}) {
		t.Fatalf("body=%x", got.Body)
	}
	if !got.PhoneLenConfident {
		t.Fatal("2019 header should win cleanly over a 2016 misread")
	}
}

func TestFindMagic(t *testing.T) {
	raw := append([]byte{0x00, 0xff}, magicBytes...)
	if idx := findMagic(raw); idx != 2 {
		t.Fatalf("idx=%d", idx)
	}
	if idx := findMagic([]byte{1, 2, 3}); idx != -1 {
		t.Fatalf("idx=%d", idx)
	}
}
