package config

import "testing"

func TestArchiveSharingDefaultAndOptOut(t *testing.T) {
	t.Setenv("SATDOWN_ARCHIVE_GROUP_WRITABLE", "")
	if !Load().ArchiveGroupWritable {
		t.Fatal("default archive must allow group write and public read")
	}
	t.Setenv("SATDOWN_ARCHIVE_GROUP_WRITABLE", "false")
	if Load().ArchiveGroupWritable {
		t.Fatal("explicit private archive policy must be respected")
	}
}
