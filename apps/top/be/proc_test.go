package top

import "testing"

// statLine builds a /proc/<pid>/stat line where the comm is the given
// string and every post-comm field's value equals its proc(5) field
// number (field 3 → "3", field 4 → "4", …). That makes any field-index
// drift in parseStat fail loudly: a value read one slot too far comes
// back as the next field's number. starttime (22) is the exception —
// it's set to 2200 so we can also check the /clockTicks divide.
func statLine(pid int, comm string) []byte {
	// Fields 3..25 by their proc(5) numbers, with starttime overridden.
	nums := []string{
		"R", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
		"14", "15", "16", "17", "18", "19", "20", "21", "2200", "23",
		"24", "25",
	}
	s := []byte{}
	s = append(s, []byte(itoa(pid))...)
	s = append(s, ' ', '(')
	s = append(s, []byte(comm)...)
	s = append(s, ')')
	for _, n := range nums {
		s = append(s, ' ')
		s = append(s, []byte(n)...)
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestParseStatFields locks the proc(5) field mapping. The historical
// bug read each field one slot too far, so RSS came back as rsslim
// (field 25, often RLIM_INFINITY) — the "funny RSS / %mem" report.
func TestParseStatFields(t *testing.T) {
	var pi ProcInfo
	if err := parseStat(statLine(1234, "cat"), &pi); err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if pi.Comm != "cat" {
		t.Errorf("Comm = %q, want cat", pi.Comm)
	}
	if pi.State != "R" {
		t.Errorf("State = %q, want R", pi.State)
	}
	if pi.PPID != 4 {
		t.Errorf("PPID = %d, want 4 (field 4)", pi.PPID)
	}
	if pi.TimeJiff != 14+15 {
		t.Errorf("TimeJiff = %d, want 29 (utime 14 + stime 15)", pi.TimeJiff)
	}
	if pi.Nice != 19 {
		t.Errorf("Nice = %d, want 19 (field 19)", pi.Nice)
	}
	if pi.Threads != 20 {
		t.Errorf("Threads = %d, want 20 (field 20)", pi.Threads)
	}
	if pi.VSize != 23 {
		t.Errorf("VSize = %d, want 23 (field 23)", pi.VSize)
	}
	if pi.RSS != 24 {
		t.Errorf("RSS = %d pages, want 24 (field 24, NOT rsslim field 25)", pi.RSS)
	}
	if pi.StartUnix != 2200/100 {
		t.Errorf("StartUnix = %d, want %d (starttime 2200 / clockTicks)", pi.StartUnix, 2200/100)
	}
}

// TestParseStatCommWithParens makes sure the last-paren cut survives a
// comm that itself contains spaces and parentheses.
func TestParseStatCommWithParens(t *testing.T) {
	var pi ProcInfo
	if err := parseStat(statLine(99, "weird (proc) name"), &pi); err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if pi.Comm != "weird (proc) name" {
		t.Errorf("Comm = %q, want %q", pi.Comm, "weird (proc) name")
	}
	if pi.RSS != 24 {
		t.Errorf("RSS = %d, want 24 (paren-comm threw off field alignment)", pi.RSS)
	}
}

// TestParseStatShort rejects a truncated line rather than indexing OOB.
func TestParseStatShort(t *testing.T) {
	var pi ProcInfo
	if err := parseStat([]byte("1 (x) R 4 5 6"), &pi); err == nil {
		t.Fatal("expected error on short stat line")
	}
}
