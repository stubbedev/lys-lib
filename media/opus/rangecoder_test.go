package opus

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The reference encoder's output (RFC 6716's celt/entenc.c and
// celt/laplace.c, compiled from the RFC) for the operation sequence
// TestRangeEncoderMatchesReference replays: the range-coded front, the
// zeroed middle and the raw bits packed from the back.
const (
	refFront = "057bf3638c83d7aa336088c49839ef4ebc5ad16c07184d2a500ef6c7ee90bdf534f66b0310bd9023de4991e067a5b0991697affa64e2f25a809f01602eca09667271de97846d4bb2cdc1ccc262282eb1ae3a4f09b4c4a3547fa661523851787e235b7ed7868dd1627ffe6afbfed799b0ff0fea4a95acd75d9dc1a82ece7cf775fe998d3dca882dab8573e17d934a19a489f09be5f1534d34b45817d17e655059788709363c76acf08fc4a9fdb422fd90f149e8bb6d9ad17a858773bbfae6d6cffeebee394a1b40df3781d0cef7711134628ed106fc0fdd5b83849760db7bde8b61031a8707b50fe8b0b769a482ed2859a1fb38fe1f19eef89c2f857d594ce67f2cca5f7ee331563dc23c8e58193f72dd6ab7b1e7edaaccd5b636f013b9a52525cf7df66ab48e437cd77f88375dbfe84bc53a695f439d65e8854cfb6bc42ee90b615a17216b21b620b54b8f1581d1b47d786f5be1922b80381e80"
	refZeros = 376
	refBack  = "309588201d667387f09a831754f26375cc75d6354f02546fedf2339919cdf1b3f2e041f9101b6d09ba82f260886c2e257b5557ed81a414cabe6dbb0ffea949134070fb81dd9a55a38e1de3252128a5e5d8dea3056b22970cb56f314e08bb11448f26e8d68311b16229f9a55548eb357db9830ecc8af4ceee28da28f8a3b9b1697f9eceaf58cc77b46b37af1f67296304cdd742e220d1f42c0d131adf045393adfdd3faf08e327bca695ae550a5b8961a6a9e4ca030aa023ace182e86739bfb56037b1037723905c3e795ca522cf006f89d200231f91fd46d2688884eea5186a477f1b68f9af233fe108de27f131582e849f5e1c384f7c79c8afe773c22faa2a51e2794241613c0c4b423ae198fba3ce7b71356b37aa62ed26baddd6da5ae3e5f52766b61c3c0a86b6ea702c11601"
)

// refTells are the reference's tell and tellFrac after every hundredth
// operation.
var refTells = [][2]int{{840, 6719}, {1625, 12995}, {2478, 19820}, {3405, 27239}, {4341, 34725}, {5178, 41420}}

func TestRangeEncoderMatchesReference(t *testing.T) {
	seed := uint32(1)
	rnd := func() uint32 {
		seed = 1664525*seed + 1013904223
		return seed >> 8
	}
	buf := make([]byte, 1024)
	for i := range buf {
		buf[i] = 0xa5
	}
	var rc rangeEncoder
	rc.reset(buf)
	for i := range 600 {
		switch rnd() % 7 {
		case 0:
			ft := 2 + rnd()%5000
			fl := rnd() % ft
			fh := fl + 1 + rnd()%(ft-fl)
			rc.encode(fl, fh, ft)
		case 1:
			b := 1 + rnd()%15
			ft := uint32(1) << b
			fl := rnd() % ft
			fh := fl + 1 + rnd()%(ft-fl)
			rc.encodeBin(fl, fh, uint(b))
		case 2:
			v := rnd() & 1
			l := 1 + rnd()%15
			rc.bitLogp(v == 1, uint(l))
		case 3:
			rc.icdf(int(rnd()%11), trimICDF, 7)
		case 4:
			ft := 2 + rnd()%(1<<23)*64
			rc.uint(rnd()%ft, ft)
		case 5:
			n := 1 + rnd()%16
			rc.bits(rnd()&(1<<n-1), uint(n))
		case 6:
			v := int(rnd()%41) - 20
			fs := (1 + rnd()%189) << 7
			decay := (1 + rnd()%178) << 6
			laplaceEncode(&rc, &v, fs, decay)
		}
		if i%100 == 99 {
			want := refTells[i/100]
			if got := [2]int{rc.tell(), rc.tellFrac()}; got != want {
				t.Fatalf("after op %d: tell, tellFrac = %v, want %v", i, got, want)
			}
		}
	}
	rc.done()
	if rc.failed {
		t.Fatal("encoder reported an overrun")
	}
	front, _ := hex.DecodeString(refFront)
	back, _ := hex.DecodeString(refBack)
	want := append(append(front, make([]byte, refZeros)...), back...)
	if len(want) != len(buf) {
		t.Fatalf("reference vector is %d bytes, buffer %d", len(want), len(buf))
	}
	if !bytes.Equal(buf, want) {
		for i := range buf {
			if buf[i] != want[i] {
				t.Fatalf("byte %d = %#02x, want %#02x", i, buf[i], want[i])
			}
		}
	}
}

func TestRangeEncoderShrinkKeepsRawBits(t *testing.T) {
	code := func(rc *rangeEncoder) {
		for i := range 40 {
			rc.encode(uint32(i%3), uint32(i%3+1), 3)
			rc.bits(uint32(i), 5)
		}
	}
	direct := make([]byte, 100)
	var rc rangeEncoder
	rc.reset(direct)
	code(&rc)
	rc.done()
	shrunk := make([]byte, 300)
	rc.reset(shrunk)
	code(&rc)
	rc.shrink(len(direct))
	rc.done()
	if rc.failed || !bytes.Equal(shrunk[:len(direct)], direct) {
		t.Fatalf("shrinking to %d bytes gave %x, want %x", len(direct), shrunk[:len(direct)], direct)
	}
}

func TestRangeEncoderReportsOverrun(t *testing.T) {
	var rc rangeEncoder
	rc.reset(make([]byte, 2))
	for range 64 {
		rc.encode(1, 2, 3)
	}
	rc.done()
	if !rc.failed {
		t.Fatal("64 trits in 2 bytes did not fail")
	}
}
