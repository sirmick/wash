package disks

import "testing"

// Combined output: `zpool list -Hp`, then @@STATUS `zpool status`, then
// @@DATASETS `zfs list -Hp`. Two pools: a healthy mirror and a degraded raidz1.
const zfsCombined = "tank\t8000000000000\t3000000000000\t5000000000000\t7\tONLINE\n" +
	"backup\t4000000000000\t1000000000000\t3000000000000\t1\tDEGRADED\n" +
	"@@STATUS\n" +
	"  pool: tank\n" +
	" state: ONLINE\n" +
	"  scan: scrub repaired 0B in 02:14:05 with 0 errors on Sun\n" +
	"config:\n" +
	"\n" +
	"\tNAME        STATE     READ WRITE CKSUM\n" +
	"\ttank        ONLINE       0     0     0\n" +
	"\t  mirror-0  ONLINE       0     0     0\n" +
	"\t    sde     ONLINE       0     0     0\n" +
	"\t    sdf     ONLINE       0     0     0\n" +
	"\n" +
	"errors: No known data errors\n" +
	"\n" +
	"  pool: backup\n" +
	" state: DEGRADED\n" +
	"  scan: resilver in progress\n" +
	"config:\n" +
	"\n" +
	"\tNAME        STATE     READ WRITE CKSUM\n" +
	"\tbackup      DEGRADED     0     0     0\n" +
	"\t  raidz1-0  DEGRADED     0     0     0\n" +
	"\t    sdg     ONLINE       0     0     0\n" +
	"\t    sdh     FAULTED      0     0     3\n" +
	"\n" +
	"@@DATASETS\n" +
	"tank\t3000000000000\t5000000000000\t98304\t/tank\n" +
	"tank/ds\t2900000000000\t5000000000000\t2900000000000\t/tank/ds\n" +
	"backup\t1000000000000\t3000000000000\t98304\t/backup\n"

func TestParseZfsCombined(t *testing.T) {
	pools := parseZfsCombined(zfsCombined)
	if len(pools) != 2 {
		t.Fatalf("pools = %d", len(pools))
	}
	byName := map[string]ZPool{}
	for _, p := range pools {
		byName[p.Name] = p
	}

	tank := byName["tank"]
	if tank.State != "ONLINE" || tank.Size != 8000000000000 || tank.Frag != 7 {
		t.Fatalf("tank = %+v", tank)
	}
	if tank.ScanStatus == "" {
		t.Fatalf("tank scan missing")
	}
	// Top-level vdev = mirror-0 with two disk children.
	if len(tank.Vdevs) != 1 || tank.Vdevs[0].Type != "mirror" || tank.Vdevs[0].Name != "mirror-0" {
		t.Fatalf("tank vdevs = %+v", tank.Vdevs)
	}
	if len(tank.Vdevs[0].Children) != 2 || tank.Vdevs[0].Children[0].Name != "sde" {
		t.Fatalf("mirror children = %+v", tank.Vdevs[0].Children)
	}
	// Datasets grouped by pool prefix.
	if len(tank.Datasets) != 2 {
		t.Fatalf("tank datasets = %+v", tank.Datasets)
	}

	backup := byName["backup"]
	if backup.State != "DEGRADED" {
		t.Fatalf("backup state = %q", backup.State)
	}
	if len(backup.Vdevs) != 1 || backup.Vdevs[0].Type != "raidz1" {
		t.Fatalf("backup vdev = %+v", backup.Vdevs)
	}
	// The faulted leaf carries a cksum error.
	var sdh ZVdev
	for _, c := range backup.Vdevs[0].Children {
		if c.Name == "sdh" {
			sdh = c
		}
	}
	if sdh.State != "FAULTED" || sdh.CksumErr != 3 {
		t.Fatalf("sdh = %+v", sdh)
	}
	if len(backup.Datasets) != 1 {
		t.Fatalf("backup datasets = %+v", backup.Datasets)
	}
}
