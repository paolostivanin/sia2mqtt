package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =====================
// calcCRC
// =====================

func TestCalcCRC_KnownVectors(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "0000"},
		{"123456789", "BB3D"},
	}
	for _, tc := range tests {
		got := calcCRC([]byte(tc.input))
		if got != tc.want {
			t.Errorf("calcCRC(%q) = %s, want %s", tc.input, got, tc.want)
		}
	}
}

func TestCalcCRC_Deterministic(t *testing.T) {
	data := []byte("hello world")
	a := calcCRC(data)
	b := calcCRC(data)
	if a != b {
		t.Errorf("calcCRC not deterministic: %s != %s", a, b)
	}
}

func TestCalcCRC_Length(t *testing.T) {
	got := calcCRC([]byte("test"))
	if len(got) != 4 {
		t.Errorf("calcCRC should return 4-char hex, got %q (len %d)", got, len(got))
	}
}

func TestCalcCRC_DifferentInputs(t *testing.T) {
	a := calcCRC([]byte("abc"))
	b := calcCRC([]byte("abd"))
	if a == b {
		t.Error("calcCRC should produce different results for different inputs")
	}
}

// =====================
// fmtLEN
// =====================

func TestFmtLEN(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0000"},
		{1, "0001"},
		{255, "00FF"},
		{4096, "1000"},
		{65535, "FFFF"},
	}
	for _, tc := range tests {
		got := fmtLEN(tc.input)
		if got != tc.want {
			t.Errorf("fmtLEN(%d) = %s, want %s", tc.input, got, tc.want)
		}
	}
}

// =====================
// parseBool
// =====================

func TestParseBool_TrueValues(t *testing.T) {
	for _, s := range []string{"1", "true", "True", "TRUE", "yes", "Yes", "on", "ON"} {
		got, err := parseBool(s)
		if err != nil {
			t.Errorf("parseBool(%q) unexpected error: %v", s, err)
		}
		if !got {
			t.Errorf("parseBool(%q) = false, want true", s)
		}
	}
}

func TestParseBool_FalseValues(t *testing.T) {
	for _, s := range []string{"0", "false", "False", "FALSE", "no", "No", "off", "OFF"} {
		got, err := parseBool(s)
		if err != nil {
			t.Errorf("parseBool(%q) unexpected error: %v", s, err)
		}
		if got {
			t.Errorf("parseBool(%q) = true, want false", s)
		}
	}
}

func TestParseBool_Invalid(t *testing.T) {
	for _, s := range []string{"", "maybe", "2", "yep"} {
		_, err := parseBool(s)
		if err == nil {
			t.Errorf("parseBool(%q) expected error, got nil", s)
		}
	}
}

func TestParseBool_Whitespace(t *testing.T) {
	got, err := parseBool("  true  ")
	if err != nil {
		t.Fatalf("parseBool with whitespace: unexpected error: %v", err)
	}
	if !got {
		t.Error("parseBool(\"  true  \") = false, want true")
	}
}

// =====================
// readFrame
// =====================

func TestReadFrame_Simple(t *testing.T) {
	input := "\r\nhello\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	frame, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame error: %v", err)
	}
	if string(frame) != "hello" {
		t.Errorf("readFrame = %q, want %q", string(frame), "hello")
	}
}

func TestReadFrame_NoLeadingCRLF(t *testing.T) {
	input := "data\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	frame, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame error: %v", err)
	}
	if string(frame) != "data" {
		t.Errorf("readFrame = %q, want %q", string(frame), "data")
	}
}

func TestReadFrame_MultipleCRLF(t *testing.T) {
	input := "\r\n\r\n\r\nframe1\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	frame, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame error: %v", err)
	}
	if string(frame) != "frame1" {
		t.Errorf("readFrame = %q, want %q", string(frame), "frame1")
	}
}

func TestReadFrame_EOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	_, err := readFrame(r)
	if err == nil {
		t.Fatal("readFrame on empty input: expected error, got nil")
	}
}

func TestReadFrame_DataThenEOF(t *testing.T) {
	input := "\ndata"
	r := bufio.NewReader(strings.NewReader(input))
	frame, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame error: %v", err)
	}
	if string(frame) != "data" {
		t.Errorf("readFrame = %q, want %q", string(frame), "data")
	}
}

func TestReadFrame_OversizeFrame(t *testing.T) {
	big := strings.Repeat("A", maxFrameSize+10)
	input := "\n" + big + "\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	_, err := readFrame(r)
	if err == nil {
		t.Fatal("readFrame: expected error for oversize frame, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Errorf("readFrame error = %v, want 'exceeds maximum size'", err)
	}
}

func TestReadFrame_MultipleFrames(t *testing.T) {
	input := "\r\nfirst\r\n\r\nsecond\r\n"
	r := bufio.NewReader(strings.NewReader(input))

	f1, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame(1) error: %v", err)
	}
	if string(f1) != "first" {
		t.Errorf("frame 1 = %q, want %q", string(f1), "first")
	}

	f2, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame(2) error: %v", err)
	}
	if string(f2) != "second" {
		t.Errorf("frame 2 = %q, want %q", string(f2), "second")
	}
}

func TestReadFrame_OnlyCRLF(t *testing.T) {
	// Only delimiters followed by EOF - first loop consumes them, second loop hits EOF
	input := "\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	_, err := readFrame(r)
	if err == nil {
		t.Fatal("readFrame on only CRLF: expected error, got nil")
	}
}

// =====================
// buildAck
// =====================

func TestBuildAck_Format(t *testing.T) {
	ack := buildAck("0001", "R0", "L0", "AAAA")
	s := string(ack)
	if !strings.HasPrefix(s, "\n") {
		t.Error("buildAck should start with \\n")
	}
	if !strings.HasSuffix(s, "\r") {
		t.Error("buildAck should end with \\r")
	}
	if !strings.Contains(s, `"ACK"`) {
		t.Error("buildAck should contain \"ACK\"")
	}
	if !strings.Contains(s, "#AAAA") {
		t.Error("buildAck should contain #AAAA")
	}
}

func TestBuildAck_CRCAndLEN(t *testing.T) {
	ack := buildAck("0001", "R0", "L0", "AAAA")
	s := string(ack)
	inner := s[1 : len(s)-1]
	crc := inner[:4]
	lenStr := inner[4:8]
	payload := inner[8:]

	wantLen := fmtLEN(len(payload))
	if lenStr != wantLen {
		t.Errorf("LEN = %s, want %s (payload len %d)", lenStr, wantLen, len(payload))
	}

	wantCRC := calcCRC([]byte(payload))
	if crc != wantCRC {
		t.Errorf("CRC = %s, want %s", crc, wantCRC)
	}
}

func TestBuildAck_DifferentAccounts(t *testing.T) {
	a1 := buildAck("0001", "R0", "L0", "AAAA")
	a2 := buildAck("0001", "R0", "L0", "BBBB")
	if string(a1) == string(a2) {
		t.Error("buildAck should produce different output for different accounts")
	}
}

func TestBuildAck_EmptyOptionals(t *testing.T) {
	// rcvrOpt and lpref can be empty
	ack := buildAck("0001", "", "", "AAAA")
	s := string(ack)
	if !strings.Contains(s, `"ACK"0001#AAAA[]`) {
		t.Errorf("buildAck with empty optionals = %q", s)
	}
}

// =====================
// reHeader regex
// =====================

func TestReHeader_ValidSIADCS(t *testing.T) {
	frame := `5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/OP502]`
	m := reHeader.FindStringSubmatch(frame)
	if len(m) < 5 {
		t.Fatalf("reHeader did not match: %q", frame)
	}
	if m[1] != "0001" {
		t.Errorf("seq = %q, want %q", m[1], "0001")
	}
	if m[2] != "R0001" {
		t.Errorf("rcvrOpt = %q, want %q", m[2], "R0001")
	}
	if m[3] != "L0001" {
		t.Errorf("lpref = %q, want %q", m[3], "L0001")
	}
	if m[4] != "AAAA" {
		t.Errorf("acct = %q, want %q", m[4], "AAAA")
	}
}

func TestReHeader_ADMCID(t *testing.T) {
	frame := `ABCD0040"ADM-CID"0002R1L2#1234[stuff]`
	m := reHeader.FindStringSubmatch(frame)
	if len(m) < 5 {
		t.Fatalf("reHeader did not match ADM-CID frame: %q", frame)
	}
	if m[4] != "1234" {
		t.Errorf("acct = %q, want %q", m[4], "1234")
	}
}

func TestReHeader_NULL(t *testing.T) {
	frame := `00000020"NULL"0003R0L0#ABC[]`
	m := reHeader.FindStringSubmatch(frame)
	if len(m) < 5 {
		t.Fatalf("reHeader did not match NULL frame: %q", frame)
	}
	if m[1] != "0003" {
		t.Errorf("seq = %q, want %q", m[1], "0003")
	}
}

func TestReHeader_StarPrefix(t *testing.T) {
	frame := `ABCD0040"*SIA-DCS"0001R0L0#AAAA[]`
	m := reHeader.FindStringSubmatch(frame)
	if len(m) < 5 {
		t.Fatalf("reHeader did not match *SIA-DCS: %q", frame)
	}
}

func TestReHeader_NoMatch(t *testing.T) {
	for _, frame := range []string{
		"this is not a valid frame",
		`"SIA-DCS"0001R0L0#AAAA[]`,  // missing CRC+LEN prefix
		`ABCD0040"UNKNOWN"0001R0L0#A`, // unsupported protocol
	} {
		m := reHeader.FindStringSubmatch(frame)
		if m != nil {
			t.Errorf("reHeader should not match %q: %v", frame, m)
		}
	}
}

func TestReHeader_LongAccountID(t *testing.T) {
	frame := `ABCD0040"SIA-DCS"0001R0L0#1234567890ABCDEF[]`
	m := reHeader.FindStringSubmatch(frame)
	if len(m) < 5 {
		t.Fatalf("reHeader did not match long account: %q", frame)
	}
	if m[4] != "1234567890ABCDEF" {
		t.Errorf("acct = %q, want %q", m[4], "1234567890ABCDEF")
	}
}

// =====================
// reEventCode regex
// =====================

func TestReEventCode_WithUser(t *testing.T) {
	frame := `|Nri1/OP502`
	m := reEventCode.FindStringSubmatch(frame)
	if len(m) < 3 {
		t.Fatalf("reEventCode did not match: %q", frame)
	}
	if m[1] != "OP" {
		t.Errorf("eventCode = %q, want %q", m[1], "OP")
	}
	if m[2] != "502" {
		t.Errorf("userID = %q, want %q", m[2], "502")
	}
}

func TestReEventCode_WithoutUser(t *testing.T) {
	frame := `|Nri1/BA`
	m := reEventCode.FindStringSubmatch(frame)
	if len(m) < 2 {
		t.Fatalf("reEventCode did not match: %q", frame)
	}
	if m[1] != "BA" {
		t.Errorf("eventCode = %q, want %q", m[1], "BA")
	}
	if len(m) > 2 && m[2] != "" {
		t.Errorf("userID = %q, want empty", m[2])
	}
}

func TestReEventCode_InFullFrame(t *testing.T) {
	frame := `5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/CL123]`
	m := reEventCode.FindStringSubmatch(frame)
	if len(m) < 3 {
		t.Fatalf("reEventCode did not match in full frame")
	}
	if m[1] != "CL" {
		t.Errorf("eventCode = %q, want %q", m[1], "CL")
	}
	if m[2] != "123" {
		t.Errorf("userID = %q, want %q", m[2], "123")
	}
}

func TestReEventCode_AllCodes(t *testing.T) {
	for code := range codeToState {
		frame := fmt.Sprintf("|Nri1/%s999", code)
		m := reEventCode.FindStringSubmatch(frame)
		if len(m) < 2 {
			t.Errorf("reEventCode did not match code %q in %q", code, frame)
			continue
		}
		if m[1] != code {
			t.Errorf("extracted code = %q, want %q", m[1], code)
		}
	}
}

func TestReEventCode_VariableDigitUsers(t *testing.T) {
	tests := []struct {
		frame    string
		wantCode string
		wantUser string
	}{
		{"|Nri1/OP1", "OP", "1"},
		{"|Nri1/OP12", "OP", "12"},
		{"|Nri1/OP123", "OP", "123"},
		{"|Nri1/OP1234", "OP", "1234"},
	}
	for _, tc := range tests {
		m := reEventCode.FindStringSubmatch(tc.frame)
		if len(m) < 3 {
			t.Errorf("reEventCode did not match %q", tc.frame)
			continue
		}
		if m[1] != tc.wantCode {
			t.Errorf("frame %q: code = %q, want %q", tc.frame, m[1], tc.wantCode)
		}
		if m[2] != tc.wantUser {
			t.Errorf("frame %q: user = %q, want %q", tc.frame, m[2], tc.wantUser)
		}
	}
}

// =====================
// codeToState map
// =====================

func TestCodeToState_Coverage(t *testing.T) {
	tests := []struct {
		code string
		want AlarmState
	}{
		{"OP", StateDisarmed},
		{"OA", StateDisarmed},
		{"OQ", StateDisarmed},
		{"OG", StateDisarmed},
		{"OR", StateDisarmed},
		{"CL", StateArmed},
		{"CA", StateArmed},
		{"CP", StateArmed},
		{"CQ", StateArmed},
		{"CF", StateArmed},
		{"NL", StateNight},
		{"NM", StateNight},
		{"NF", StateNight},
		{"BA", StateAlarm},
		{"TA", StateAlarm},
		{"FA", StateAlarm},
		{"PA", StateAlarm},
		{"HA", StateAlarm},
		{"MA", StateAlarm},
		{"BV", StateAlarm},
		{"GA", StateAlarm},
		{"WA", StateAlarm},
		{"KA", StateAlarm},
	}
	for _, tc := range tests {
		got, ok := codeToState[tc.code]
		if !ok {
			t.Errorf("codeToState[%q] not found", tc.code)
			continue
		}
		if got != tc.want {
			t.Errorf("codeToState[%q] = %q, want %q", tc.code, got, tc.want)
		}
	}
	// Verify we tested all entries
	if len(tests) != len(codeToState) {
		t.Errorf("test covers %d codes, but codeToState has %d entries", len(tests), len(codeToState))
	}
}

func TestCodeToState_UnknownCode(t *testing.T) {
	_, ok := codeToState["ZZ"]
	if ok {
		t.Error("codeToState[\"ZZ\"] should not exist")
	}
}

// =====================
// sanitizeBrokerURL
// =====================

func TestSanitizeBrokerURL_NoCredentials(t *testing.T) {
	got := sanitizeBrokerURL("tcp://127.0.0.1:1883")
	if got != "tcp://127.0.0.1:1883" {
		t.Errorf("sanitizeBrokerURL = %q, want %q", got, "tcp://127.0.0.1:1883")
	}
}

func TestSanitizeBrokerURL_WithUserOnly(t *testing.T) {
	got := sanitizeBrokerURL("tcp://myuser@broker:1883")
	if !strings.Contains(got, "myuser@") {
		t.Errorf("sanitizeBrokerURL should keep username, got %q", got)
	}
}

func TestSanitizeBrokerURL_WithPassword(t *testing.T) {
	got := sanitizeBrokerURL("tcp://myuser:secret@broker:1883")
	if strings.Contains(got, "secret") {
		t.Errorf("sanitizeBrokerURL should redact password, got %q", got)
	}
	if !strings.Contains(got, "myuser@") {
		t.Errorf("sanitizeBrokerURL should keep username, got %q", got)
	}
}

func TestSanitizeBrokerURL_InvalidURL(t *testing.T) {
	raw := "://not-a-url"
	got := sanitizeBrokerURL(raw)
	if got != raw {
		t.Errorf("sanitizeBrokerURL(%q) = %q, want original", raw, got)
	}
}

func TestSanitizeBrokerURL_TLS(t *testing.T) {
	got := sanitizeBrokerURL("tls://user:pass@broker:8883/path")
	if strings.Contains(got, "pass") {
		t.Errorf("sanitizeBrokerURL should redact password, got %q", got)
	}
	if !strings.Contains(got, "user@") {
		t.Errorf("sanitizeBrokerURL should keep username, got %q", got)
	}
}

func TestSanitizeBrokerURL_PlainBroker(t *testing.T) {
	got := sanitizeBrokerURL("tcp://localhost:1883")
	if got != "tcp://localhost:1883" {
		t.Errorf("sanitizeBrokerURL = %q", got)
	}
}

// =====================
// zeroOrRFC3339
// =====================

func TestZeroOrRFC3339_Zero(t *testing.T) {
	got := zeroOrRFC3339(time.Time{})
	if got != nil {
		t.Errorf("zeroOrRFC3339(zero) = %v, want nil", got)
	}
}

func TestZeroOrRFC3339_NonZero(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	got := zeroOrRFC3339(ts)
	s, ok := got.(string)
	if !ok {
		t.Fatalf("zeroOrRFC3339 returned %T, want string", got)
	}
	if !strings.Contains(s, "2026-01-15") {
		t.Errorf("zeroOrRFC3339 = %q, does not contain date", s)
	}
	// Must be parseable as RFC3339
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		t.Errorf("zeroOrRFC3339 output not valid RFC3339: %v", err)
	}
}

// =====================
// loadConfig
// =====================

func TestLoadConfig_Defaults(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("# empty config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}

	if cfg.SIAListenAddr != ":45128" {
		t.Errorf("SIAListenAddr = %q, want %q", cfg.SIAListenAddr, ":45128")
	}
	if cfg.MQTTBroker != "tcp://127.0.0.1:1883" {
		t.Errorf("MQTTBroker = %q, want default", cfg.MQTTBroker)
	}
	if cfg.MQTTTopic != "home/alarm/ajax/state" {
		t.Errorf("MQTTTopic = %q, want default", cfg.MQTTTopic)
	}
	if cfg.MQTTBaseTopic != "home/alarm/ajax" {
		t.Errorf("MQTTBaseTopic = %q, want %q", cfg.MQTTBaseTopic, "home/alarm/ajax")
	}
	if !cfg.MQTTRetain {
		t.Error("MQTTRetain should default to true")
	}
	if !cfg.MQTTDiscoveryEnable {
		t.Error("MQTTDiscoveryEnable should default to true")
	}
	if cfg.MQTTQOS != 0 {
		t.Errorf("MQTTQOS = %d, want 0", cfg.MQTTQOS)
	}
	if cfg.LogMaxSizeMB != 10 {
		t.Errorf("LogMaxSizeMB = %d, want 10", cfg.LogMaxSizeMB)
	}
	if cfg.LogMaxBackups != 5 {
		t.Errorf("LogMaxBackups = %d, want 5", cfg.LogMaxBackups)
	}
	if cfg.LogMaxAgeDays != 30 {
		t.Errorf("LogMaxAgeDays = %d, want 30", cfg.LogMaxAgeDays)
	}
	if cfg.Verbose {
		t.Error("Verbose should default to false")
	}
	if cfg.MQTTInsecureSkipVerify {
		t.Error("MQTTInsecureSkipVerify should default to false")
	}
}

func TestLoadConfig_OverrideValues(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	content := `sia_listen=:9999
sia_account_id=BBBB
mqtt_broker=tcp://10.0.0.1:1883
mqtt_topic=my/custom/topic
mqtt_qos=2
mqtt_retain=false
verbose=true
`
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}

	if cfg.SIAListenAddr != ":9999" {
		t.Errorf("SIAListenAddr = %q, want %q", cfg.SIAListenAddr, ":9999")
	}
	if cfg.SIAAccountID != "BBBB" {
		t.Errorf("SIAAccountID = %q, want %q", cfg.SIAAccountID, "BBBB")
	}
	if cfg.MQTTBroker != "tcp://10.0.0.1:1883" {
		t.Errorf("MQTTBroker = %q", cfg.MQTTBroker)
	}
	if cfg.MQTTTopic != "my/custom/topic" {
		t.Errorf("MQTTTopic = %q", cfg.MQTTTopic)
	}
	if cfg.MQTTBaseTopic != "my/custom" {
		t.Errorf("MQTTBaseTopic = %q, want %q", cfg.MQTTBaseTopic, "my/custom")
	}
	if cfg.MQTTQOS != 2 {
		t.Errorf("MQTTQOS = %d, want 2", cfg.MQTTQOS)
	}
	if cfg.MQTTRetain {
		t.Error("MQTTRetain should be false")
	}
	if !cfg.Verbose {
		t.Error("Verbose should be true")
	}
}

func TestLoadConfig_AllKeys(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	content := `sia_listen=:12345
sia_account_id=ZZZZ
http_listen=0.0.0.0:9090
state_file=/tmp/state.json
mqtt_broker=tls://broker:8883
mqtt_user=testuser
mqtt_password=testpass
mqtt_clientid=myclient
mqtt_topic=a/b/c/d
mqtt_qos=1
mqtt_retain=true
mqtt_availability_topic=a/b/avail
mqtt_availability_online=up
mqtt_availability_offline=down
mqtt_discovery_enable=false
mqtt_discovery_prefix=hass
mqtt_discovery_node_id=mynode
mqtt_discovery_object_id=myobj
mqtt_discovery_name=My Sensor
mqtt_tls_insecure_skip_verify=true
log_file=/tmp/test.log
log_max_size_mb=50
log_max_backups=3
log_max_age_days=7
verbose=true
`
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}

	if cfg.SIAListenAddr != ":12345" {
		t.Errorf("SIAListenAddr = %q", cfg.SIAListenAddr)
	}
	if cfg.SIAAccountID != "ZZZZ" {
		t.Errorf("SIAAccountID = %q", cfg.SIAAccountID)
	}
	if cfg.HTTPListenAddr != "0.0.0.0:9090" {
		t.Errorf("HTTPListenAddr = %q", cfg.HTTPListenAddr)
	}
	if cfg.StateFile != "/tmp/state.json" {
		t.Errorf("StateFile = %q", cfg.StateFile)
	}
	if cfg.MQTTBroker != "tls://broker:8883" {
		t.Errorf("MQTTBroker = %q", cfg.MQTTBroker)
	}
	if cfg.MQTTUser != "testuser" {
		t.Errorf("MQTTUser = %q", cfg.MQTTUser)
	}
	if cfg.MQTTPass != "testpass" {
		t.Errorf("MQTTPass = %q", cfg.MQTTPass)
	}
	if cfg.MQTTClientID != "myclient" {
		t.Errorf("MQTTClientID = %q", cfg.MQTTClientID)
	}
	if cfg.MQTTTopic != "a/b/c/d" {
		t.Errorf("MQTTTopic = %q", cfg.MQTTTopic)
	}
	if cfg.MQTTBaseTopic != "a/b/c" {
		t.Errorf("MQTTBaseTopic = %q", cfg.MQTTBaseTopic)
	}
	if cfg.MQTTQOS != 1 {
		t.Errorf("MQTTQOS = %d", cfg.MQTTQOS)
	}
	if !cfg.MQTTRetain {
		t.Error("MQTTRetain should be true")
	}
	if cfg.MQTTAvailabilityTopic != "a/b/avail" {
		t.Errorf("MQTTAvailabilityTopic = %q", cfg.MQTTAvailabilityTopic)
	}
	if cfg.MQTTAvailabilityOn != "up" {
		t.Errorf("MQTTAvailabilityOn = %q", cfg.MQTTAvailabilityOn)
	}
	if cfg.MQTTAvailabilityOff != "down" {
		t.Errorf("MQTTAvailabilityOff = %q", cfg.MQTTAvailabilityOff)
	}
	if cfg.MQTTDiscoveryEnable {
		t.Error("MQTTDiscoveryEnable should be false")
	}
	if cfg.MQTTDiscoveryPrefix != "hass" {
		t.Errorf("MQTTDiscoveryPrefix = %q", cfg.MQTTDiscoveryPrefix)
	}
	if cfg.MQTTDiscoveryNodeID != "mynode" {
		t.Errorf("MQTTDiscoveryNodeID = %q", cfg.MQTTDiscoveryNodeID)
	}
	if cfg.MQTTDiscoveryObject != "myobj" {
		t.Errorf("MQTTDiscoveryObject = %q", cfg.MQTTDiscoveryObject)
	}
	if cfg.MQTTDiscoveryName != "My Sensor" {
		t.Errorf("MQTTDiscoveryName = %q", cfg.MQTTDiscoveryName)
	}
	if !cfg.MQTTInsecureSkipVerify {
		t.Error("MQTTInsecureSkipVerify should be true")
	}
	if cfg.LogFile != "/tmp/test.log" {
		t.Errorf("LogFile = %q", cfg.LogFile)
	}
	if cfg.LogMaxSizeMB != 50 {
		t.Errorf("LogMaxSizeMB = %d", cfg.LogMaxSizeMB)
	}
	if cfg.LogMaxBackups != 3 {
		t.Errorf("LogMaxBackups = %d", cfg.LogMaxBackups)
	}
	if cfg.LogMaxAgeDays != 7 {
		t.Errorf("LogMaxAgeDays = %d", cfg.LogMaxAgeDays)
	}
	if !cfg.Verbose {
		t.Error("Verbose should be true")
	}
}

func TestLoadConfig_UnknownKey(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("unknown_key=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(cfgFile)
	if err == nil {
		t.Fatal("loadConfig should reject unknown keys")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error = %v, want 'unknown key'", err)
	}
}

func TestLoadConfig_InvalidQOS(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")

	for _, val := range []string{"5", "-1", "abc"} {
		if err := os.WriteFile(cfgFile, []byte("mqtt_qos="+val+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadConfig(cfgFile)
		if err == nil {
			t.Errorf("loadConfig should reject qos=%s", val)
		}
	}
}

func TestLoadConfig_InvalidBool(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("verbose=maybe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(cfgFile)
	if err == nil {
		t.Fatal("loadConfig should reject invalid bool")
	}
}

func TestLoadConfig_InvalidLine(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("no_equals_sign\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(cfgFile)
	if err == nil {
		t.Fatal("loadConfig should reject lines without =")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.conf")
	if err == nil {
		t.Fatal("loadConfig should error on missing file")
	}
}

func TestLoadConfig_CommentsAndBlanks(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	content := `# This is a comment
sia_listen=:5555

# Another comment

mqtt_broker=tcp://localhost:1883
`
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}
	if cfg.SIAListenAddr != ":5555" {
		t.Errorf("SIAListenAddr = %q, want %q", cfg.SIAListenAddr, ":5555")
	}
}

func TestLoadConfig_MQTTBaseTopic_NoSlash(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("mqtt_topic=topiconly\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}
	if cfg.MQTTBaseTopic != "topiconly" {
		t.Errorf("MQTTBaseTopic = %q, want %q", cfg.MQTTBaseTopic, "topiconly")
	}
}

func TestLoadConfig_InvalidLogMaxSizeMB(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	for _, val := range []string{"0", "-5", "abc"} {
		if err := os.WriteFile(cfgFile, []byte("log_max_size_mb="+val+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := loadConfig(cfgFile)
		if err == nil {
			t.Errorf("loadConfig should reject log_max_size_mb=%s", val)
		}
	}
}

func TestLoadConfig_InvalidLogMaxBackups(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("log_max_backups=-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(cfgFile)
	if err == nil {
		t.Fatal("loadConfig should reject negative log_max_backups")
	}
}

func TestLoadConfig_InvalidLogMaxAgeDays(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	if err := os.WriteFile(cfgFile, []byte("log_max_age_days=-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(cfgFile)
	if err == nil {
		t.Fatal("loadConfig should reject negative log_max_age_days")
	}
}

func TestLoadConfig_CaseInsensitiveKeys(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	content := "SIA_LISTEN=:7777\nMQTT_QOS=1\n"
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}
	if cfg.SIAListenAddr != ":7777" {
		t.Errorf("SIAListenAddr = %q, want %q", cfg.SIAListenAddr, ":7777")
	}
	if cfg.MQTTQOS != 1 {
		t.Errorf("MQTTQOS = %d, want 1", cfg.MQTTQOS)
	}
}

func TestLoadConfig_ValueWithEquals(t *testing.T) {
	tmp := t.TempDir()
	cfgFile := filepath.Join(tmp, "test.conf")
	// Password containing = should be preserved
	content := "mqtt_password=abc=def=\n"
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}
	if cfg.MQTTPass != "abc=def=" {
		t.Errorf("MQTTPass = %q, want %q", cfg.MQTTPass, "abc=def=")
	}
}

// =====================
// RuntimeStats
// =====================

func TestRuntimeStats_UpdateAndSnapshot(t *testing.T) {
	s := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	s.UpdateEvent(StateArmed, "CL", "502", "raw frame")

	snap := s.Snapshot()
	if snap.State != StateArmed {
		t.Errorf("State = %q, want %q", snap.State, StateArmed)
	}
	if snap.Code != "CL" {
		t.Errorf("Code = %q, want %q", snap.Code, "CL")
	}
	if snap.User != "502" {
		t.Errorf("User = %q, want %q", snap.User, "502")
	}
	if snap.LastEventTime.IsZero() {
		t.Error("LastEventTime should not be zero")
	}
}

func TestRuntimeStats_ApplyPersistedState(t *testing.T) {
	s := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}

	ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	s.ApplyPersistedState(PersistedState{
		State:         StateNight,
		Code:          "NL",
		User:          "100",
		LastEventTime: ts,
	})

	snap := s.Snapshot()
	if snap.State != StateNight {
		t.Errorf("State = %q, want %q", snap.State, StateNight)
	}
	if snap.Code != "NL" {
		t.Errorf("Code = %q, want %q", snap.Code, "NL")
	}
	if snap.User != "100" {
		t.Errorf("User = %q, want %q", snap.User, "100")
	}
	if !snap.LastEventTime.Equal(ts) {
		t.Errorf("LastEventTime = %v, want %v", snap.LastEventTime, ts)
	}
}

func TestRuntimeStats_ApplyPersistedState_PartialEmpty(t *testing.T) {
	s := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	s.UpdateEvent(StateArmed, "CL", "502", "raw")

	s.ApplyPersistedState(PersistedState{
		State: StateDisarmed,
	})

	snap := s.Snapshot()
	if snap.State != StateDisarmed {
		t.Errorf("State = %q, want %q", snap.State, StateDisarmed)
	}
	if snap.Code != "CL" {
		t.Errorf("Code should remain %q, got %q", "CL", snap.Code)
	}
	if snap.User != "502" {
		t.Errorf("User should remain %q, got %q", "502", snap.User)
	}
}

func TestRuntimeStats_UpdateMQTTPublish(t *testing.T) {
	s := &RuntimeStats{StartTime: time.Now()}
	s.UpdateMQTTPublish(true)
	s.UpdateMQTTPublish(true)
	s.UpdateMQTTPublish(false)

	if atomic.LoadUint64(&s.MQTTPubOK) != 2 {
		t.Errorf("MQTTPubOK = %d, want 2", atomic.LoadUint64(&s.MQTTPubOK))
	}
	if atomic.LoadUint64(&s.MQTTPubErr) != 1 {
		t.Errorf("MQTTPubErr = %d, want 1", atomic.LoadUint64(&s.MQTTPubErr))
	}
}

func TestRuntimeStats_ConcurrentAccess(t *testing.T) {
	s := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			code := fmt.Sprintf("C%d", n%10)
			s.UpdateEvent(StateArmed, code, "1", "raw")
			s.Snapshot()
			s.UpdateMQTTPublish(true)
			atomic.AddUint64(&s.FramesRx, 1)
			atomic.AddInt64(&s.ActiveConn, 1)
			atomic.AddInt64(&s.ActiveConn, -1)
		}(i)
	}
	wg.Wait()

	if atomic.LoadUint64(&s.FramesRx) != 100 {
		t.Errorf("FramesRx = %d, want 100", atomic.LoadUint64(&s.FramesRx))
	}
	if atomic.LoadInt64(&s.ActiveConn) != 0 {
		t.Errorf("ActiveConn = %d, want 0", atomic.LoadInt64(&s.ActiveConn))
	}
}

// =====================
// persistState / loadPersistedState
// =====================

func TestPersistAndLoadState(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	stats.UpdateEvent(StateAlarm, "BA", "100", "raw")

	if err := persistState(stateFile, stats); err != nil {
		t.Fatalf("persistState error: %v", err)
	}

	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	var ps PersistedState
	if err := json.Unmarshal(data, &ps); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if ps.State != StateAlarm {
		t.Errorf("persisted state = %q, want %q", ps.State, StateAlarm)
	}
	if ps.Code != "BA" {
		t.Errorf("persisted code = %q, want %q", ps.Code, "BA")
	}

	stats2 := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	if err := loadPersistedState(stateFile, stats2); err != nil {
		t.Fatalf("loadPersistedState error: %v", err)
	}
	snap := stats2.Snapshot()
	if snap.State != StateAlarm {
		t.Errorf("loaded state = %q, want %q", snap.State, StateAlarm)
	}
	if snap.User != "100" {
		t.Errorf("loaded user = %q, want %q", snap.User, "100")
	}
}

func TestPersistState_EmptyState(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	stats := &RuntimeStats{StartTime: time.Now()}
	if err := persistState(stateFile, stats); err != nil {
		t.Fatalf("persistState error: %v", err)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Error("persistState should not create file for empty state")
	}
}

func TestPersistState_EmptyPath(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now()}
	stats.UpdateEvent(StateArmed, "CL", "1", "raw")
	if err := persistState("", stats); err != nil {
		t.Errorf("persistState with empty path should return nil, got: %v", err)
	}
}

func TestPersistState_CreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "sub", "dir", "state.json")

	stats := &RuntimeStats{StartTime: time.Now()}
	stats.UpdateEvent(StateArmed, "CL", "1", "raw")

	if err := persistState(stateFile, stats); err != nil {
		t.Fatalf("persistState error: %v", err)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state file should exist: %v", err)
	}
}

func TestPersistState_Overwrite(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")

	stats := &RuntimeStats{StartTime: time.Now()}
	stats.UpdateEvent(StateArmed, "CL", "1", "raw")
	if err := persistState(stateFile, stats); err != nil {
		t.Fatalf("persistState(1) error: %v", err)
	}

	stats.UpdateEvent(StateAlarm, "BA", "2", "raw2")
	if err := persistState(stateFile, stats); err != nil {
		t.Fatalf("persistState(2) error: %v", err)
	}

	stats2 := &RuntimeStats{}
	if err := loadPersistedState(stateFile, stats2); err != nil {
		t.Fatalf("loadPersistedState error: %v", err)
	}
	snap := stats2.Snapshot()
	if snap.State != StateAlarm {
		t.Errorf("overwritten state = %q, want %q", snap.State, StateAlarm)
	}
	if snap.Code != "BA" {
		t.Errorf("overwritten code = %q, want %q", snap.Code, "BA")
	}
}

func TestLoadPersistedState_EmptyPath(t *testing.T) {
	stats := &RuntimeStats{}
	if err := loadPersistedState("", stats); err != nil {
		t.Errorf("loadPersistedState with empty path should return nil, got: %v", err)
	}
}

func TestLoadPersistedState_FileNotFound(t *testing.T) {
	stats := &RuntimeStats{}
	if err := loadPersistedState("/nonexistent/state.json", stats); err != nil {
		t.Errorf("loadPersistedState for missing file should return nil, got: %v", err)
	}
}

func TestLoadPersistedState_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	stateFile := filepath.Join(tmp, "state.json")
	if err := os.WriteFile(stateFile, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats := &RuntimeStats{}
	err := loadPersistedState(stateFile, stats)
	if err == nil {
		t.Fatal("loadPersistedState should error on invalid JSON")
	}
}

// =====================
// Publisher.SyncFromStats
// =====================

func TestPublisher_SyncFromStats(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now()}
	stats.UpdateEvent(StateNight, "NL", "200", "raw")

	pub := &Publisher{stats: stats}
	pub.SyncFromStats(stats)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if pub.lastState != StateNight {
		t.Errorf("lastState = %q, want %q", pub.lastState, StateNight)
	}
	if pub.lastCode != "NL" {
		t.Errorf("lastCode = %q, want %q", pub.lastCode, "NL")
	}
	if pub.lastUser != "200" {
		t.Errorf("lastUser = %q, want %q", pub.lastUser, "200")
	}
}

func TestPublisher_SyncFromStats_Empty(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	pub := &Publisher{stats: stats}
	pub.SyncFromStats(stats)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if pub.lastState != StateUnknown {
		t.Errorf("lastState = %q, want %q", pub.lastState, StateUnknown)
	}
	if pub.lastCode != "" {
		t.Errorf("lastCode = %q, want empty", pub.lastCode)
	}
}

// =====================
// Publisher topic helpers
// =====================

func TestPublisher_UserTopic(t *testing.T) {
	pub := &Publisher{
		cfg: Config{
			MQTTTopic:     "home/alarm/ajax/state",
			MQTTBaseTopic: "home/alarm/ajax",
		},
	}
	got := pub.userTopic()
	if got != "home/alarm/ajax/user" {
		t.Errorf("userTopic = %q, want %q", got, "home/alarm/ajax/user")
	}
}

func TestPublisher_UserTopic_CustomTopic(t *testing.T) {
	pub := &Publisher{
		cfg: Config{
			MQTTTopic:     "my/custom/topic",
			MQTTBaseTopic: "my/custom",
		},
	}
	got := pub.userTopic()
	if got != "my/custom/user" {
		t.Errorf("userTopic = %q, want %q", got, "my/custom/user")
	}
}

func TestPublisher_DiscoveryConfigTopic(t *testing.T) {
	pub := &Publisher{
		cfg: Config{
			MQTTDiscoveryPrefix: "homeassistant",
			MQTTDiscoveryNodeID: "ajax",
			MQTTDiscoveryObject: "alarm_state",
		},
	}
	got := pub.discoveryConfigTopic()
	want := "homeassistant/sensor/ajax/alarm_state/config"
	if got != want {
		t.Errorf("discoveryConfigTopic = %q, want %q", got, want)
	}
}

func TestPublisher_DiscoveryConfigTopic_TrailingSlash(t *testing.T) {
	pub := &Publisher{
		cfg: Config{
			MQTTDiscoveryPrefix: "homeassistant/",
			MQTTDiscoveryNodeID: "ajax",
			MQTTDiscoveryObject: "alarm_state",
		},
	}
	got := pub.discoveryConfigTopic()
	want := "homeassistant/sensor/ajax/alarm_state/config"
	if got != want {
		t.Errorf("discoveryConfigTopic = %q, want %q", got, want)
	}
}

func TestPublisher_UserDiscoveryConfigTopic(t *testing.T) {
	pub := &Publisher{
		cfg: Config{
			MQTTDiscoveryPrefix: "homeassistant",
			MQTTDiscoveryNodeID: "ajax",
		},
	}
	got := pub.userDiscoveryConfigTopic()
	want := "homeassistant/sensor/ajax/user/config"
	if got != want {
		t.Errorf("userDiscoveryConfigTopic = %q, want %q", got, want)
	}
}

func TestPublisher_UniqueID(t *testing.T) {
	pub := &Publisher{
		cfg: Config{
			MQTTDiscoveryNodeID: "ajax",
			MQTTDiscoveryObject: "alarm_state",
		},
	}
	got := pub.uniqueID()
	want := "ajax_alarm_state"
	if got != want {
		t.Errorf("uniqueID = %q, want %q", got, want)
	}
}

// =====================
// Publisher dedup (no MQTT client needed)
// =====================

// TestPublisher_PublishState_FailedPublishDoesNotPoisonDedup is a regression
// guard: a failed publish (here: no MQTT client at all, so publishStateMessage
// returns an error) MUST NOT update the dedup state. Otherwise the next
// identical event would be silently skipped — but the broker never received
// the first one. The bug existed before this fix.
func TestPublisher_PublishState_FailedPublishDoesNotPoisonDedup(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now()}
	pub := &Publisher{cfg: Config{}, stats: stats}

	// First call: publish will fail (no client). Dedup must remain empty.
	pub.PublishState(StateArmed, "CL")
	pub.mu.Lock()
	gotState, gotCode := pub.lastState, pub.lastCode
	pub.mu.Unlock()
	if gotState != "" || gotCode != "" {
		t.Errorf("dedup must NOT be updated after failed publish, got state=%q code=%q", gotState, gotCode)
	}

	// Stats: one error counted, no successes.
	if atomic.LoadUint64(&stats.MQTTPubErr) != 1 {
		t.Errorf("MQTTPubErr = %d, want 1", atomic.LoadUint64(&stats.MQTTPubErr))
	}
	if atomic.LoadUint64(&stats.MQTTPubOK) != 0 {
		t.Errorf("MQTTPubOK = %d, want 0", atomic.LoadUint64(&stats.MQTTPubOK))
	}

	// Second call with same args: must attempt publish again (and fail again),
	// since the previous failure didn't update dedup.
	pub.PublishState(StateArmed, "CL")
	if atomic.LoadUint64(&stats.MQTTPubErr) != 2 {
		t.Errorf("MQTTPubErr = %d, want 2 (retry must happen)", atomic.LoadUint64(&stats.MQTTPubErr))
	}
}

// TestPublisher_PublishUser_FailedPublishDoesNotPoisonDedup mirrors the state
// test for the user topic.
func TestPublisher_PublishUser_FailedPublishDoesNotPoisonDedup(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now()}
	pub := &Publisher{cfg: Config{}, stats: stats}

	pub.PublishUser("502")
	pub.mu.Lock()
	gotUser := pub.lastUser
	pub.mu.Unlock()
	if gotUser != "" {
		t.Errorf("dedup must NOT be updated after failed publish, got user=%q", gotUser)
	}
}

// TestPublisher_RepublishCurrent_NoState is a smoke test for RepublishCurrent
// when there is no persisted state — it must be a no-op (and crucially must
// not panic on a nil client).
func TestPublisher_RepublishCurrent_NoState(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	pub := &Publisher{cfg: Config{}, stats: stats}
	// snap.State is "" (LastState was set to "unknown" but Snapshot reads
	// from LastState directly — the test for empty State path is when stats
	// has no LastState set at all).
	stats2 := &RuntimeStats{StartTime: time.Now()}
	pub2 := &Publisher{cfg: Config{}, stats: stats2}
	pub2.RepublishCurrent() // should return early, no error
	if atomic.LoadUint64(&stats2.MQTTPubErr) != 0 {
		t.Error("RepublishCurrent with no state should not record any pub error")
	}
	_ = pub
}

// =====================
// HTTP endpoints
// =====================

func TestHTTP_Health(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	pub := &Publisher{cfg: Config{HTTPListenAddr: "127.0.0.1:0"}, stats: stats}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok\n" {
		t.Errorf("body = %q, want %q", string(body), "ok\n")
	}
	_ = pub // used for setup context
}

func TestHTTP_State(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	stats.UpdateEvent(StateArmed, "CL", "502", "raw")

	mux := http.NewServeMux()
	mux.HandleFunc("/state", func(w http.ResponseWriter, _ *http.Request) {
		stats.mu.RLock()
		lastState := stats.LastState
		lastCode := stats.LastCode
		lastUser := stats.LastUser
		lastEventTime := stats.LastEventTime
		stats.mu.RUnlock()

		out := map[string]any{
			"state":           lastState,
			"code":            lastCode,
			"user":            lastUser,
			"last_event_time": zeroOrRFC3339(lastEventTime),
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/state")
	if err != nil {
		t.Fatalf("GET /state error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if result["state"] != "armed" {
		t.Errorf("state = %v, want armed", result["state"])
	}
	if result["code"] != "CL" {
		t.Errorf("code = %v, want CL", result["code"])
	}
	if result["user"] != "502" {
		t.Errorf("user = %v, want 502", result["user"])
	}
	if result["last_event_time"] == nil {
		t.Error("last_event_time should not be nil")
	}
}

func TestHTTP_Stats(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	stats.UpdateEvent(StateDisarmed, "OP", "100", "raw frame")
	atomic.AddUint64(&stats.FramesRx, 5)
	atomic.AddUint64(&stats.AcksTx, 3)

	pub := &Publisher{
		cfg: Config{
			MQTTDiscoveryPrefix: "homeassistant",
			MQTTDiscoveryNodeID: "ajax",
			MQTTDiscoveryObject: "alarm_state",
		},
		stats: stats,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		uptime := time.Since(stats.StartTime)

		stats.mu.RLock()
		lastState := stats.LastState
		lastCode := stats.LastCode
		lastUser := stats.LastUser
		lastRaw := stats.LastEventRaw
		lastEventTime := stats.LastEventTime
		lastMQTTPubTime := stats.LastMQTTPublishTime
		stats.mu.RUnlock()

		out := map[string]any{
			"uptime_seconds":             int64(uptime.Seconds()),
			"start_time":                 stats.StartTime.Format(time.RFC3339),
			"active_connections":         atomic.LoadInt64(&stats.ActiveConn),
			"frames_rx":                  atomic.LoadUint64(&stats.FramesRx),
			"acks_tx":                    atomic.LoadUint64(&stats.AcksTx),
			"rejected_account_mismatch":  atomic.LoadUint64(&stats.RejectedAccountMismatch),
			"mqtt_connected":             pub.IsConnected(),
			"mqtt_pub_ok":                atomic.LoadUint64(&stats.MQTTPubOK),
			"mqtt_pub_err":               atomic.LoadUint64(&stats.MQTTPubErr),
			"last_state":                 lastState,
			"last_code":                  lastCode,
			"last_user":                  lastUser,
			"last_event_time":            zeroOrRFC3339(lastEventTime),
			"last_mqtt_pub_time":         zeroOrRFC3339(lastMQTTPubTime),
			"last_event_raw":             lastRaw,
			"ha_discovery_state_topic":   pub.discoveryConfigTopic(),
			"ha_discovery_user_topic":    pub.userDiscoveryConfigTopic(),
			"ha_unique_id":               pub.uniqueID(),
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("GET /stats error: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if result["last_state"] != "disarmed" {
		t.Errorf("last_state = %v, want disarmed", result["last_state"])
	}
	if result["frames_rx"] != float64(5) {
		t.Errorf("frames_rx = %v, want 5", result["frames_rx"])
	}
	if result["acks_tx"] != float64(3) {
		t.Errorf("acks_tx = %v, want 3", result["acks_tx"])
	}
	if result["mqtt_connected"] != false {
		t.Errorf("mqtt_connected = %v, want false", result["mqtt_connected"])
	}
	if result["ha_discovery_state_topic"] != "homeassistant/sensor/ajax/alarm_state/config" {
		t.Errorf("ha_discovery_state_topic = %v", result["ha_discovery_state_topic"])
	}
	if result["ha_unique_id"] != "ajax_alarm_state" {
		t.Errorf("ha_unique_id = %v", result["ha_unique_id"])
	}
}

// =====================
// handleConnection (integration via net.Pipe)
// =====================

func waitForCondition(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleConnection_ValidFrame(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{
		SIAAccountID:  "AAAA",
		MQTTBaseTopic: "home/alarm/ajax",
		StateFile:     filepath.Join(t.TempDir(), "state.json"),
	}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	// Send a valid SIA DC-09 frame
	frame := `5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/OP502]`
	_, err := client.Write([]byte("\n" + frame + "\r"))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Read the ACK response
	buf := make([]byte, 1024)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("read ACK error: %v", err)
	}
	ack := string(buf[:n])
	if !strings.Contains(ack, `"ACK"`) {
		t.Errorf("expected ACK, got %q", ack)
	}

	// Close client so handleConnection finishes processing and returns
	client.Close()
	<-done

	// Verify stats after handleConnection has fully completed
	if atomic.LoadUint64(&stats.FramesRx) != 1 {
		t.Errorf("FramesRx = %d, want 1", atomic.LoadUint64(&stats.FramesRx))
	}
	if atomic.LoadUint64(&stats.AcksTx) != 1 {
		t.Errorf("AcksTx = %d, want 1", atomic.LoadUint64(&stats.AcksTx))
	}

	snap := stats.Snapshot()
	if snap.State != StateDisarmed {
		t.Errorf("state = %q, want %q", snap.State, StateDisarmed)
	}
	if snap.Code != "OP" {
		t.Errorf("code = %q, want %q", snap.Code, "OP")
	}
	if snap.User != "502" {
		t.Errorf("user = %q, want %q", snap.User, "502")
	}
}

func TestHandleConnection_AccountMismatch(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{
		SIAAccountID: "AAAA",
		StateFile:    "",
	}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	// Send frame with wrong account
	frame := `5AB50053"SIA-DCS"0001R0001L0001#BBBB[#BBBB|Nri1/OP502]`
	_, _ = client.Write([]byte("\n" + frame + "\r"))

	// Should NOT receive an ACK - wait briefly then check
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1024)
	_, err := client.Read(buf)
	if err == nil {
		t.Error("should not receive ACK for mismatched account")
	}

	if atomic.LoadUint64(&stats.RejectedAccountMismatch) != 1 {
		t.Errorf("RejectedAccountMismatch = %d, want 1", atomic.LoadUint64(&stats.RejectedAccountMismatch))
	}

	client.Close()
	<-done
}

func TestHandleConnection_InvalidFrame(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{StateFile: ""}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	// Send garbage that doesn't match reHeader
	_, _ = client.Write([]byte("\nthis is not a valid SIA frame\r"))

	// No ACK expected
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1024)
	_, err := client.Read(buf)
	if err == nil {
		t.Error("should not receive ACK for invalid frame")
	}

	// FramesRx should increment (it counts all non-empty frames)
	if atomic.LoadUint64(&stats.FramesRx) != 1 {
		t.Errorf("FramesRx = %d, want 1", atomic.LoadUint64(&stats.FramesRx))
	}
	// AcksTx should NOT increment
	if atomic.LoadUint64(&stats.AcksTx) != 0 {
		t.Errorf("AcksTx = %d, want 0", atomic.LoadUint64(&stats.AcksTx))
	}

	client.Close()
	<-done
}

func TestHandleConnection_MultipleFrames(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{
		SIAAccountID:  "AAAA",
		MQTTBaseTopic: "test",
		StateFile:     filepath.Join(t.TempDir(), "state.json"),
	}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	frames := []struct {
		frame string
		code  string
	}{
		{`5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/OP502]`, "OP"},
		{`5AB50053"SIA-DCS"0002R0001L0001#AAAA[#AAAA|Nri1/CL502]`, "CL"},
	}

	buf := make([]byte, 1024)
	for _, f := range frames {
		_, err := client.Write([]byte("\n" + f.frame + "\r"))
		if err != nil {
			t.Fatalf("write error: %v", err)
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := client.Read(buf)
		if err != nil {
			t.Fatalf("read ACK error: %v", err)
		}
		if !strings.Contains(string(buf[:n]), `"ACK"`) {
			t.Errorf("expected ACK for frame with code %s", f.code)
		}
	}

	// Close client and wait for handleConnection to finish all processing
	client.Close()
	<-done

	if atomic.LoadUint64(&stats.FramesRx) != 2 {
		t.Errorf("FramesRx = %d, want 2", atomic.LoadUint64(&stats.FramesRx))
	}

	snap := stats.Snapshot()
	if snap.State != StateArmed {
		t.Errorf("final state = %q, want %q", snap.State, StateArmed)
	}
}

func TestHandleConnection_ClientDisconnect(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{StateFile: ""}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	// Close client immediately
	client.Close()

	// handleConnection should return promptly
	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("handleConnection did not return after client disconnect")
	}

	if atomic.LoadInt64(&stats.ActiveConn) != 0 {
		t.Errorf("ActiveConn = %d, want 0", atomic.LoadInt64(&stats.ActiveConn))
	}
}

// =====================
// verifyFrameChecksum
// =====================

func TestVerifyFrameChecksum_Valid(t *testing.T) {
	// Build a frame from a known payload so we know the expected CRC and LEN.
	payload := `"SIA-DCS"0001R0L0#AAAA[#AAAA|Nri1/OP502]`
	crc := calcCRC([]byte(payload))
	lf := fmtLEN(len(payload))
	frame := crc + lf + payload
	if err := verifyFrameChecksum(frame); err != nil {
		t.Errorf("verifyFrameChecksum returned error on valid frame: %v", err)
	}
}

func TestVerifyFrameChecksum_BadCRC(t *testing.T) {
	payload := `"SIA-DCS"0001R0L0#AAAA[]`
	lf := fmtLEN(len(payload))
	frame := "DEAD" + lf + payload
	err := verifyFrameChecksum(frame)
	if err == nil || !strings.Contains(err.Error(), "CRC mismatch") {
		t.Errorf("expected CRC mismatch error, got %v", err)
	}
}

func TestVerifyFrameChecksum_BadLEN(t *testing.T) {
	payload := `"SIA-DCS"0001R0L0#AAAA[]`
	crc := calcCRC([]byte(payload))
	frame := crc + "FFFF" + payload
	err := verifyFrameChecksum(frame)
	if err == nil || !strings.Contains(err.Error(), "LEN mismatch") {
		t.Errorf("expected LEN mismatch error, got %v", err)
	}
}

func TestVerifyFrameChecksum_TooShort(t *testing.T) {
	if err := verifyFrameChecksum("AB"); err == nil {
		t.Error("expected error for short frame")
	}
}

func TestVerifyFrameChecksum_NonHexLEN(t *testing.T) {
	payload := `"SIA-DCS"0001R0L0#AAAA[]`
	crc := calcCRC([]byte(payload))
	frame := crc + "GHIJ" + payload
	if err := verifyFrameChecksum(frame); err == nil {
		t.Error("expected error for non-hex LEN")
	}
}

// =====================
// redactAccountID
// =====================

func TestRedactAccountID_RedactsBothOccurrences(t *testing.T) {
	raw := `5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/OP502]`
	got := redactAccountID(raw, "AAAA")
	if strings.Contains(got, "AAAA") {
		t.Errorf("AAAA should have been redacted, got %q", got)
	}
	if strings.Count(got, "#REDACTED") != 2 {
		t.Errorf("expected 2 #REDACTED occurrences, got %q", got)
	}
}

func TestRedactAccountID_EmptyAcct(t *testing.T) {
	raw := "some frame"
	got := redactAccountID(raw, "")
	if got != raw {
		t.Errorf("empty acct should be no-op, got %q", got)
	}
}

func TestRedactAccountID_NoMatch(t *testing.T) {
	raw := `5AB50053"SIA-DCS"0001R0001L0001#BBBB[]`
	got := redactAccountID(raw, "AAAA")
	if got != raw {
		t.Errorf("non-matching acct should leave raw unchanged, got %q", got)
	}
}

// =====================
// HTTP method restriction
// =====================

func TestOnlyGET_AllowsGetAndHead(t *testing.T) {
	called := false
	h := onlyGET(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		called = false
		req := httptest.NewRequest(method, "/", nil)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", method, w.Code)
		}
		if !called {
			t.Errorf("%s: handler should have been called", method)
		}
	}
}

func TestOnlyGET_RejectsOtherMethods(t *testing.T) {
	h := onlyGET(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/", nil)
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, w.Code)
		}
		if got := w.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s: Allow header = %q, want %q", method, got, "GET, HEAD")
		}
	}
}

// =====================
// handleConnection: invalid frame counter + verbose-gated log
// =====================

func TestHandleConnection_InvalidFrame_IncrementsCounter(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{StateFile: "", SIAReadTimeoutSeconds: 60}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	_, _ = client.Write([]byte("\nthis is not a valid SIA frame\r"))
	// Brief pause so the handler processes the frame.
	_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 1024)
	_, _ = client.Read(buf)

	client.Close()
	<-done

	if got := atomic.LoadUint64(&stats.InvalidFrames); got != 1 {
		t.Errorf("InvalidFrames = %d, want 1", got)
	}
}

// =====================
// handleConnection: account ID redaction in stored raw
// =====================

func TestHandleConnection_RedactsAccountInStoredRaw(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{
		SIAAccountID:          "AAAA",
		MQTTBaseTopic:         "test",
		StateFile:             filepath.Join(t.TempDir(), "state.json"),
		SIAReadTimeoutSeconds: 60,
	}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	frame := `5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/OP502]`
	_, _ = client.Write([]byte("\n" + frame + "\r"))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	_, _ = client.Read(buf)

	client.Close()
	<-done

	stats.mu.RLock()
	stored := stats.LastEventRaw
	stats.mu.RUnlock()

	if strings.Contains(stored, "AAAA") {
		t.Errorf("LastEventRaw must not contain account ID, got %q", stored)
	}
	if !strings.Contains(stored, "#REDACTED") {
		t.Errorf("LastEventRaw should contain #REDACTED, got %q", stored)
	}
}

// =====================
// handleConnection: SIAVerifyCRC rejects bad CRC frames
// =====================

func TestHandleConnection_VerifyCRC_RejectsBadCRC(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{
		SIAAccountID:          "AAAA",
		SIAVerifyCRC:          true,
		StateFile:             "",
		SIAReadTimeoutSeconds: 60,
	}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	// Bogus CRC (5AB5) on a frame whose real CRC is different — should be
	// rejected when SIAVerifyCRC is true.
	frame := `5AB50053"SIA-DCS"0001R0001L0001#AAAA[#AAAA|Nri1/OP502]`
	_, _ = client.Write([]byte("\n" + frame + "\r"))
	_ = client.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1024)
	_, err := client.Read(buf)
	if err == nil {
		t.Error("should not receive ACK when SIAVerifyCRC rejects the frame")
	}

	client.Close()
	<-done

	if got := atomic.LoadUint64(&stats.InvalidFrames); got != 1 {
		t.Errorf("InvalidFrames = %d, want 1", got)
	}
	if got := atomic.LoadUint64(&stats.AcksTx); got != 0 {
		t.Errorf("AcksTx = %d, want 0 (no ACK on CRC failure)", got)
	}
}

func TestHandleConnection_VerifyCRC_AcceptsValidCRC(t *testing.T) {
	stats := &RuntimeStats{StartTime: time.Now(), LastState: StateUnknown}
	cfg := Config{
		SIAAccountID:          "AAAA",
		SIAVerifyCRC:          true,
		MQTTBaseTopic:         "test",
		StateFile:             filepath.Join(t.TempDir(), "state.json"),
		SIAReadTimeoutSeconds: 60,
	}
	pub := &Publisher{cfg: cfg, stats: stats}

	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		handleConnection(server, cfg, pub, stats)
		close(done)
	}()

	// Build a frame with a correct CRC and LEN.
	payload := `"SIA-DCS"0001R0L0#AAAA[#AAAA|Nri1/OP502]`
	frame := calcCRC([]byte(payload)) + fmtLEN(len(payload)) + payload
	_, _ = client.Write([]byte("\n" + frame + "\r"))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("expected ACK, got err: %v", err)
	}
	if !strings.Contains(string(buf[:n]), `"ACK"`) {
		t.Errorf("expected ACK, got %q", string(buf[:n]))
	}

	client.Close()
	<-done

	if got := atomic.LoadUint64(&stats.AcksTx); got != 1 {
		t.Errorf("AcksTx = %d, want 1", got)
	}
}

// =====================
// AlarmState constants
// =====================

func TestAlarmStateConstants(t *testing.T) {
	if StateUnknown != "unknown" {
		t.Errorf("StateUnknown = %q", StateUnknown)
	}
	if StateDisarmed != "disarmed" {
		t.Errorf("StateDisarmed = %q", StateDisarmed)
	}
	if StateArmed != "armed" {
		t.Errorf("StateArmed = %q", StateArmed)
	}
	if StateNight != "night" {
		t.Errorf("StateNight = %q", StateNight)
	}
	if StateAlarm != "alarm" {
		t.Errorf("StateAlarm = %q", StateAlarm)
	}
}
