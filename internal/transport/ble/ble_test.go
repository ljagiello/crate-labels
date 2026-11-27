package ble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/crate-labels/internal/printer"
	"github.com/ljagiello/crate-labels/internal/transport/fake"
)

const (
	ff02 = "0000ff02-0000-1000-8000-00805f9b34fb"
	ff03 = "0000ff03-0000-1000-8000-00805f9b34fb"
	abf1 = "0000abf1-0000-1000-8000-00805f9b34fb"
	abf3 = "0000abf3-0000-1000-8000-00805f9b34fb"
	abf4 = "0000abf4-0000-1000-8000-00805f9b34fb"
)

func catalog(t *testing.T) *printer.Catalog {
	t.Helper()
	c, err := printer.LoadCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func phomemoProfile(t *testing.T) *printer.BLEProfile {
	t.Helper()
	m, err := catalog(t).Model("phomemo-m110")
	if err != nil {
		t.Fatal(err)
	}
	return m.BLE
}

// phomemoDevice is an FF00 printer with FF02 (write) and FF03 (notify).
func phomemoDevice(name string, mtu ...int) (*fakePeripheral, *fakeChar, *fakeChar) {
	w := &fakeChar{uuid: ff02, mtu: mtu}
	n := &fakeChar{uuid: strings.ToUpper(ff03)} // case must not matter
	return newPeripheral(name).add(PhomemoService, w, n), w, n
}

func openFake(t *testing.T, p *fakePeripheral) *Link {
	t.Helper()
	install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"dev": p}})
	l, err := Open(context.Background(), "dev", phomemoProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestOpenMapsRolesAndCapsMaxWrite(t *testing.T) {
	for _, tc := range []struct {
		mtu  int
		want int
	}{{244, 128}, {97, 97}, {12, 20}} {
		p, w, n := phomemoDevice("M110", tc.mtu)
		l := openFake(t, p)
		if got := l.MaxWrite(); got != tc.want {
			t.Errorf("mtu %d: MaxWrite = %d, want %d", tc.mtu, got, tc.want)
		}
		if !n.subscribed() {
			t.Error("FF03 was not subscribed")
		}
		if err := l.Write(context.Background(), []byte{1, 2}, "data", false); err != nil {
			t.Fatal(err)
		}
		if len(w.writes) != 1 || w.resp[0] {
			t.Errorf("writes = %v response %v", w.writes, w.resp)
		}
		if l.Name() != "M110" || l.Address() != "dev" || len(l.Services()) != 1 {
			t.Errorf("name %q address %q services %v", l.Name(), l.Address(), l.Services())
		}
	}
}

func TestMTUWaitsForTheRealValue(t *testing.T) {
	p, _, _ := phomemoDevice("M110", 20, 20, 182)
	l := openFake(t, p)
	if got := l.MaxWrite(); got != 128 {
		t.Errorf("MaxWrite = %d, want 128 once the MTU settles", got)
	}
}

func TestNotificationsAreQueuedInOrder(t *testing.T) {
	p, _, n := phomemoDevice("M110")
	l := openFake(t, p)
	n.notify([]byte{0x01, 0x07})
	n.notify([]byte{0x1a, 0x07, 0x02})
	ctx := context.Background()
	for _, want := range [][]byte{{0x01, 0x07}, {0x1a, 0x07, 0x02}} {
		got, err := l.Recv(ctx, time.Second)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("recv = % x, %v; want % x", got, err, want)
		}
	}
	if _, err := l.Recv(ctx, 20*time.Millisecond); !errors.Is(err, printer.ErrTimeout) {
		t.Errorf("empty inbox err = %v, want ErrTimeout", err)
	}
}

func TestInboxDropsOldestWhenFull(t *testing.T) {
	p, _, n := phomemoDevice("M110")
	l := openFake(t, p)
	for i := range inboxFrames + 3 {
		n.notify([]byte{byte(i)})
	}
	got, _ := l.Recv(context.Background(), time.Second)
	if got[0] != 3 {
		t.Errorf("first queued frame = %d, want 3 (the 3 oldest dropped)", got[0])
	}
}

func TestDisconnectWakesRecvAndFailsWrites(t *testing.T) {
	p, _, n := phomemoDevice("M110")
	l := openFake(t, p)
	n.notify([]byte{9})
	errc := make(chan error, 1)
	go func() {
		_, _ = l.Recv(context.Background(), 5*time.Second) // the queued frame
		_, err := l.Recv(context.Background(), 5*time.Second)
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	p.drop()
	select {
	case err := <-errc:
		if err == nil || errors.Is(err, printer.ErrTimeout) {
			t.Errorf("recv after disconnect = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not wake on disconnect")
	}
	err := l.Write(context.Background(), []byte{1}, "data", false)
	if err == nil || !strings.Contains(err.Error(), "disconnected mid-print") {
		t.Errorf("write after disconnect = %v", err)
	}
	if l.Connected() {
		t.Error("Connected after a drop")
	}
}

func TestCloseIsIdempotentAndUnsubscribes(t *testing.T) {
	p, _, n := phomemoDevice("M110")
	l := openFake(t, p)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if n.subscribed() || p.disconnectCount() != 1 {
		t.Errorf("subscribed %v, disconnects %d", n.subscribed(), p.disconnectCount())
	}
}

func TestOpenErrors(t *testing.T) {
	profile := phomemoProfile(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		dev  *fakePeripheral
		want string
	}{
		{"missing service", newPeripheral("x").add(MunbynService, &fakeChar{uuid: abf1}), "does not offer the 0000ff00 service"},
		{"missing write", newPeripheral("x").add(PhomemoService, &fakeChar{uuid: ff03}), "no 0000ff02 characteristic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"dev": tc.dev}})
			_, err := Open(ctx, "dev", profile)
			var pe *printer.Error
			if !errors.As(err, &pe) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if tc.dev.disconnectCount() != 1 {
				t.Error("a failed open must disconnect")
			}
		})
	}
	t.Run("missing notify still connects", func(t *testing.T) {
		dev := newPeripheral("x").add(PhomemoService, &fakeChar{uuid: ff02})
		install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"dev": dev}})
		l, err := Open(ctx, "dev", profile)
		if err != nil {
			t.Fatal(err)
		}
		l.Close()
	})
	t.Run("unreachable", func(t *testing.T) {
		b := &fakeBackend{connectErr: errors.New("timeout on Connect")}
		install(t, b)
		_, err := Open(ctx, "dev", profile)
		if err == nil || !strings.Contains(err.Error(), "No answer from the printer after 20 s") {
			t.Fatalf("err = %v", err)
		}
		if b.timeouts[0] != ConnectTimeout {
			t.Errorf("connect timeout %v", b.timeouts[0])
		}
	})
	t.Run("deadline shortens the connect", func(t *testing.T) {
		b := &fakeBackend{connectErr: errors.New("timeout")}
		install(t, b)
		dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, _ = Open(dctx, "dev", profile)
		if b.timeouts[0] > 2*time.Second {
			t.Errorf("connect timeout %v, want ≤ the context's 2 s", b.timeouts[0])
		}
	})
	t.Run("no backend", func(t *testing.T) {
		install(t, nil)
		_, err := Open(ctx, "dev", profile)
		if err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWriteErrorsBecomePrinterErrors(t *testing.T) {
	p, w, _ := phomemoDevice("M110")
	l := openFake(t, p)
	w.failWith = errors.New("bluetooth: write without response timed out")
	err := l.Write(context.Background(), []byte{1}, "data", false)
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "Bluetooth write failed") {
		t.Errorf("err = %v", err)
	}
	if err := l.Write(context.Background(), []byte{1}, "image", false); err == nil || !strings.Contains(err.Error(), `no "image" characteristic`) {
		t.Errorf("unknown role err = %v", err)
	}
	w.failWith = nil
	if err := l.Write(context.Background(), []byte{7}, "data", true); err != nil || !w.resp[len(w.resp)-1] {
		t.Errorf("write with response: %v", err)
	}
}

func TestOpenerIsRegistered(t *testing.T) {
	p, w, _ := phomemoDevice("M110")
	install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"AAAA-BBBB": p}})
	m, _ := catalog(t).Model("phomemo-m110")
	l, err := printer.OpenLink(context.Background(), &printer.Target{Name: "M110", Address: "AAAA-BBBB"}, m)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Write(context.Background(), []byte{0x1f, 0x11, 0x07}, "data", false); err != nil || len(w.writes) != 1 {
		t.Fatalf("write through the opener: %v", err)
	}
	if _, err := printer.OpenLink(context.Background(), &printer.Target{Name: "M110"}, m); err == nil {
		t.Error("an empty address must fail")
	}
}

func TestChunkSizeIsCappedByTheLink(t *testing.T) {
	for _, tc := range []struct{ max, chunk, want int }{{244, 128, 128}, {97, 128, 97}, {244, 0, 244}, {20, 128, 20}} {
		link := fake.New(tc.max)
		if err := WriteChunked(context.Background(), link, make([]byte, 300), "data", false, tc.chunk, 0); err != nil {
			t.Fatal(err)
		}
		if got := len(link.Writes()[0].Data); got != tc.want {
			t.Errorf("max %d chunk %d: first write %d, want %d", tc.max, tc.chunk, got, tc.want)
		}
	}
}

func TestChunkerSplitsAJobIntoProfileSizedWrites(t *testing.T) {
	// A 3-row job on a 384-dot head is 171 bytes: one 128-byte write, then 43 bytes.
	var gaps []time.Duration
	link := fake.New(244)
	c := Chunker{Role: "data", Chunk: 128, Gap: 20 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error { gaps = append(gaps, d); return nil }}
	n, err := c.Write(context.Background(), link, make([]byte, 171))
	if err != nil {
		t.Fatal(err)
	}
	w := link.Writes()
	if n != 2 || len(w) != 2 || len(w[0].Data) != 128 || len(w[1].Data) != 43 {
		t.Fatalf("%d writes", len(w))
	}
	if len(gaps) != 1 || gaps[0] != 20*time.Millisecond {
		t.Errorf("gaps = %v, want one 20ms gap between the two writes", gaps)
	}
}

func TestChunkedRolesProgressAndNoGapAfterLast(t *testing.T) {
	var sent []int
	var gaps int
	link := fake.New(244)
	c := Chunker{Role: "image", WithResponse: true, Chunk: 10, Gap: time.Millisecond,
		Sleep:   func(context.Context, time.Duration) error { gaps++; return nil },
		OnChunk: func(n, total int) { sent = append(sent, n) }}
	data := bytes.Repeat([]byte{0xab}, 25)
	if _, err := c.Write(context.Background(), link, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(link.Sent("image"), data) || !link.Writes()[0].WithResponse {
		t.Error("data or flags not passed through")
	}
	if len(sent) != 3 || sent[2] != 25 || gaps != 2 {
		t.Errorf("progress %v, gaps %d", sent, gaps)
	}
}

// failingLink fails after n writes.
type failingLink struct {
	*fake.Link
	n int
}

func (l *failingLink) Write(ctx context.Context, data []byte, role string, withResponse bool) error {
	if l.n == 0 {
		return printer.Errorf(disconnectedMessage)
	}
	l.n--
	return l.Link.Write(ctx, data, role, withResponse)
}

func TestChunkedFailureNamesTheChunk(t *testing.T) {
	err := WriteChunked(context.Background(), &failingLink{fake.New(100), 2}, make([]byte, 450), "data", false, 100, 0)
	if err == nil || err.Error() != "Write failed at chunk 3 of 5: The printer disconnected mid-print." {
		t.Errorf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = WriteChunked(ctx, fake.New(100), make([]byte, 450), "data", false, 100, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled gap err = %v", err)
	}
}

func TestClassify(t *testing.T) {
	cat := catalog(t)
	for _, tc := range []struct {
		name     string
		services []string
		models   string
		maybe    string
	}{
		// No service in the advert: the name alone.
		{"M110-1A2B", nil, "phomemo-m110", ""},
		{"Phomemo M220", nil, "phomemo-m220", ""},
		{"D30-xyz", nil, "phomemo-d30", ""},
		{"CT320B_99", nil, "clabel-ct320b", ""},
		{"Phomemo printer", nil, "", ""}, // a brand is not a model
		{"Q199E1A2B", nil, "", ""},
		{"", nil, "", ""},
		// A catalog service: it decides the family, the name picks the model.
		{"M110-1A2B", []string{"ff00"}, "phomemo-m110", ""},
		{"Speaker", []string{"ff00"}, "", "phomemo"},
		{"Speaker", []string{MunbynService}, "", "munbyn"},
		{"M110-1A2B", []string{MunbynService}, "", "munbyn"}, // the service outranks the name
		// The shared UART service counts only with a matching name.
		{"Speaker", []string{ClabelService}, "", ""},
		{"CT-230B", []string{ClabelService}, "clabel-ct230b", ""},
		{"M110-1A2B", []string{ClabelService}, "phomemo-m110", ""},
	} {
		d := Classify(Advertisement{Address: "a", Name: tc.name, ServiceUUIDs: tc.services}, cat)
		if got := strings.Join(d.ModelIDs, ","); got != tc.models || d.MaybeFamily != tc.maybe {
			t.Errorf("%q %v: models %q maybe %q, want %q %q", tc.name, tc.services, got, d.MaybeFamily, tc.models, tc.maybe)
		}
	}
}

func TestServiceUUIDBreaksNameTies(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir+"/printers/clash.toml", `
[[model]]
id = "other-m110"
label = "Other M110"
family = "munbyn"
link = "ble"
dpi = 203.2
head_dots = 384
[model.ble]
name_patterns = ["m110"]
service = "0000abf0-0000-1000-8000-00805f9b34fb"
write = { command = "0000abf1-0000-1000-8000-00805f9b34fb" }
`); err != nil {
		t.Fatal(err)
	}
	cat, err := printer.LoadCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	both := Classify(Advertisement{Name: "M110"}, cat)
	ff00 := Classify(Advertisement{Name: "M110", ServiceUUIDs: []string{"FF00"}}, cat)
	if len(both.ModelIDs) != 2 || strings.Join(ff00.ModelIDs, ",") != "phomemo-m110" {
		t.Errorf("no service %v, FF00 %v", both.ModelIDs, ff00.ModelIDs)
	}
}

func TestScanFiltersMergesAndSorts(t *testing.T) {
	cat := catalog(t)
	install(t, &fakeBackend{adverts: []Advertisement{
		{Address: "u1", Name: "", RSSI: -80},
		{Address: "u2", Name: "Headphones", RSSI: -40},
		{Address: "u3", Name: "M220-77", RSSI: -70},
		{Address: "u1", Name: "M110-1A2B", RSSI: -50, ServiceUUIDs: []string{"ff00"}},
		{Address: "u1", Name: "", RSSI: -55}, // a scan response without a name keeps it
	}})
	got, err := Scan(context.Background(), 30*time.Millisecond, cat)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != "u1" || got[0].Name != "M110-1A2B" || got[0].RSSI != -55 || got[1].Address != "u3" {
		t.Fatalf("scan = %+v", got)
	}
	if got[0].ServiceUUIDs[0] != PhomemoService {
		t.Errorf("services = %v", got[0].ServiceUUIDs)
	}
	all, err := ScanAll(context.Background(), 30*time.Millisecond, cat)
	if err != nil || len(all) != 3 || all[0].Name != "Headphones" {
		t.Errorf("scan all = %+v, %v", all, err)
	}
}

func TestScanExplainsBluetoothProblems(t *testing.T) {
	install(t, &fakeBackend{scanErr: errors.New("bluetooth is powered off")})
	_, err := Scan(context.Background(), time.Second, catalog(t))
	var pe *printer.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "Privacy & Security > Bluetooth") {
		t.Errorf("err = %v", err)
	}
}

func TestDiscoveredJSON(t *testing.T) {
	b, _ := json.Marshal(Discovered{Address: "u1", Name: "M110", RSSI: -50})
	want := `{"address":"u1","name":"M110","rssi":-50,"service_uuids":[],"model_ids":[],"maybe_family":null}`
	if string(b) != want {
		t.Errorf("json = %s", b)
	}
	b, _ = json.Marshal(ProbeResult{Services: []string{PhomemoService}, Handshake: []byte{1, 7}, Family: "phomemo"})
	want = `{"services":["` + PhomemoService + `"],"handshake":"0107","family":"phomemo","model_id":null}`
	if string(b) != want {
		t.Errorf("json = %s", b)
	}
}

func TestProbeFamilyFromServicesAndName(t *testing.T) {
	cat := catalog(t)
	for _, tc := range []struct {
		services []string
		name     string
		family   string
		model    string
	}{
		// A model whose service is offered and whose name matches decides.
		{[]string{ClabelService, MunbynService, PhomemoService}, "CT320B", "clabel", "clabel-ct320b"},
		{[]string{ClabelService, MunbynService}, "CT320B", "clabel", "clabel-ct320b"},
		{[]string{ClabelService}, "CT320B_1", "clabel", "clabel-ct320b"},
		// Without a matching name, the first offered service in catalog order, and
		// its model when it is the only one on that service.
		{[]string{PhomemoService, MunbynService}, "Speaker", "munbyn", "munbyn-fm226"},
		{[]string{MunbynService}, "FM-something", "munbyn", "munbyn-fm226"},
		// The shared UART service alone, or no catalog service at all, is not a printer.
		{[]string{ClabelService}, "Speaker", "", ""},
		{[]string{"0000180a-0000-1000-8000-00805f9b34fb"}, "M110", "", ""},
	} {
		dev := newPeripheral(tc.name)
		for _, s := range tc.services {
			dev.add(s, &fakeChar{uuid: ff03})
		}
		install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"dev": dev}})
		res, err := Probe(context.Background(), "dev", cat)
		if err != nil {
			t.Fatal(err)
		}
		if res.Family != tc.family || res.ModelID != tc.model || len(res.Services) != len(tc.services) {
			t.Errorf("%v %q: %+v, want %q %q", tc.services, tc.name, res, tc.family, tc.model)
		}
		if dev.disconnectCount() != 1 {
			t.Error("Probe must disconnect")
		}
	}
}

func TestProbeCapturesTheHelloForRegisteredFamilies(t *testing.T) {
	cat := catalog(t)
	var gotFrame []byte
	var gotName string
	var gotKnown *printer.Model
	RegisterModelResolver("phomemo", func(frame []byte, name string, known *printer.Model, models []*printer.Model) *printer.Model {
		gotFrame, gotName, gotKnown = frame, name, known
		if len(models) != 3 {
			t.Errorf("resolver got %d phomemo models", len(models))
		}
		return models[1]
	})
	t.Cleanup(func() {
		resolverMu.Lock()
		delete(resolvers, "phomemo")
		resolverMu.Unlock()
	})
	dev, w, _ := phomemoDevice("OS name")
	dev.onSubscribe = func(c *fakeChar) {
		time.Sleep(10 * time.Millisecond)
		c.notify(nil) // empty frames are skipped
		c.notify([]byte{0x01, 0x07})
	}
	install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"dev": dev}})
	res, err := ProbeHint(context.Background(), "dev", cat, Hint{Name: "M220-1", KnownModel: "phomemo-d30"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Handshake, []byte{0x01, 0x07}) || res.ModelID != "phomemo-m220" || res.Family != "phomemo" {
		t.Errorf("probe = %+v", res)
	}
	if !bytes.Equal(gotFrame, []byte{0x01, 0x07}) || gotName != "M220-1" || gotKnown == nil || gotKnown.ID != "phomemo-d30" {
		t.Errorf("resolver got % x %q %v", gotFrame, gotName, gotKnown)
	}
	if len(w.writes) != 0 {
		t.Error("Probe must not write to the printer")
	}
}

func TestProbeWithoutHandshakeUsesTheOSName(t *testing.T) {
	cat := catalog(t)
	dev := newPeripheral("FM226-ABC").add(MunbynService, &fakeChar{uuid: abf1}, &fakeChar{uuid: abf3}, &fakeChar{uuid: abf4})
	install(t, &fakeBackend{peripherals: map[string]*fakePeripheral{"dev": dev}})
	res, err := Probe(context.Background(), "dev", cat)
	if err != nil || res.ModelID != "munbyn-fm226" || res.Handshake != nil {
		t.Errorf("probe = %+v, %v", res, err)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"FF00": PhomemoService, "0000ABF0": MunbynService, ClabelService: ClabelService} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q", in, got)
		}
	}
}

func TestSleep(t *testing.T) {
	if err := Sleep(context.Background(), 0); err != nil {
		t.Errorf("zero sleep = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, -time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("non-positive sleep on a cancelled context = %v", err)
	}
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled sleep = %v", err)
	}
}
