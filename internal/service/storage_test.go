package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

func testSettings(t *testing.T) Settings {
	t.Helper()
	root := filepath.Join("..", "..")
	dataDir := t.TempDir()
	return Settings{
		DataDir:     dataDir,
		RawDir:      filepath.Join(dataDir, "raw_mysekai"),
		TmpDir:      filepath.Join(dataDir, "tmp"),
		ArchiveDir:  filepath.Join(dataDir, "archive"),
		LatestDir:   filepath.Join(dataDir, "latest"),
		ResourceCSV: filepath.Join(root, "assets", "resourceId.csv"),
		FontFile:    filepath.Join(root, "assets", "NotoSansSC-Regular.ttf"),
	}
}

func TestChunkStoreMergesOutOfOrderAndCleansUp(t *testing.T) {
	store, err := NewChunkStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if merged, complete, err := store.Add("job_1", 1, 3, []byte("middle")); err != nil || complete || merged != nil {
		t.Fatalf("first add: merged=%q complete=%v err=%v", merged, complete, err)
	}
	if _, complete, err := store.Add("job_1", 2, 3, []byte("end")); err != nil || complete {
		t.Fatalf("second add complete=%v err=%v", complete, err)
	}
	merged, complete, err := store.Add("job_1", 0, 3, []byte("start-"))
	if err != nil || !complete || string(merged) != "start-middleend" {
		t.Fatalf("merged=%q complete=%v err=%v", merged, complete, err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "job_1")); err != nil {
		t.Fatalf("retryable chunk directory missing: %v", err)
	}
	if err := store.MarkSubmitted("job_1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add("job_1", 0, 3, []byte("start-")); err != ErrChunkAlreadySubmitted {
		t.Fatalf("got %v, want submitted marker", err)
	}
	if err := store.Complete("job_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "job_1")); !os.IsNotExist(err) {
		t.Fatalf("chunk directory remains after completion: %v", err)
	}
}

func TestChunkStoreRejectsMismatchedAndOversizedData(t *testing.T) {
	store, err := NewChunkStore(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add("same", 0, 2, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add("same", 1, 3, []byte("b")); err != ErrChunkTotalMismatch {
		t.Fatalf("got %v", err)
	}
	if _, _, err := store.Add("large", 0, 1, []byte("12345")); err != ErrChunkTotalTooLarge {
		t.Fatalf("got %v", err)
	}
}

type blockingProcessor struct {
	started  chan string
	release  <-chan struct{}
	finished chan string
}

func (p *blockingProcessor) Process(_ context.Context, _ string, _ string, taskID string) error {
	p.started <- taskID
	<-p.release
	p.finished <- taskID
	return nil
}

func TestAsyncSubmitterQueuesAndDrainsJobs(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	processor := &blockingProcessor{
		started:  make(chan string, 3),
		release:  release,
		finished: make(chan string, 3),
	}
	submitter := &AsyncSubmitter{Store: store, Processor: processor, Workers: 1}
	if err := submitter.Submit(context.Background(), []byte("one"), "42", "task1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("first job did not start")
	}
	for _, taskID := range []string{"task2", "task3"} {
		if err := submitter.Submit(context.Background(), []byte(taskID), "42", taskID); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(store.PendingDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got pending raw files %v, want all accepted jobs", entries)
	}
	close(release)
	for index := 0; index < 3; index++ {
		select {
		case <-processor.finished:
		case <-time.After(time.Second):
			t.Fatal("queued job did not finish")
		}
	}
	submitter.Close()
	if err := submitter.Submit(context.Background(), []byte("late"), "42", "task4"); err != ErrSubmitterClosed {
		t.Fatalf("got %v, want closed submitter", err)
	}
	if names := regularFileNames(t, store.PendingDir); len(names) != 0 {
		t.Fatalf("processed archives left pending: %v", names)
	}
	if names := regularFileNames(t, store.RawDir); len(names) != 3 {
		t.Fatalf("got processed raw files %v, want 3 in the raw directory", names)
	}
}

func regularFileNames(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	return names
}

type panicThenProcessor struct {
	mu        sync.Mutex
	calls     int
	completed chan struct{}
}

func (p *panicThenProcessor) Process(_ context.Context, _ string, _ string, _ string) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		panic("synthetic processor panic")
	}
	close(p.completed)
	return nil
}

func TestAsyncSubmitterWorkerSurvivesProcessorPanic(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	processor := &panicThenProcessor{completed: make(chan struct{})}
	submitter := &AsyncSubmitter{Store: store, Processor: processor, Workers: 1}
	if err := submitter.Submit(context.Background(), []byte("first"), "42", "panic1"); err != nil {
		t.Fatal(err)
	}
	if err := submitter.Submit(context.Background(), []byte("second"), "42", "panic2"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-processor.completed:
	case <-time.After(time.Second):
		t.Fatal("worker did not continue after panic")
	}
	submitter.Close()
	failed := regularFileNames(t, store.FailedDir)
	if len(failed) != 1 || !strings.Contains(failed[0], "panic1") {
		t.Fatalf("failed raw files=%v, want the panicking job", failed)
	}
	processed := regularFileNames(t, store.RawDir)
	if len(processed) != 1 || !strings.Contains(processed[0], "panic2") {
		t.Fatalf("processed raw files=%v, want the successful job", processed)
	}
}

type recordingProcessor struct {
	mu   sync.Mutex
	jobs []string
	err  error
}

func (p *recordingProcessor) Process(_ context.Context, rawPath, userID, taskID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := os.Stat(rawPath); err != nil {
		return err
	}
	p.jobs = append(p.jobs, userID+"/"+taskID)
	return p.err
}

func TestAsyncSubmitterRecoversPendingArchives(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	// Archives accepted by a previous run that stopped before processing them.
	for _, task := range []string{"old1", "old2"} {
		if _, err := store.SaveRaw([]byte(task), "42", task); err != nil {
			t.Fatal(err)
		}
	}
	// A processed archive from the old layout must not be reprocessed.
	if err := os.WriteFile(filepath.Join(store.RawDir, "mysekai_42_legacy.bin"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	processor := &recordingProcessor{}
	submitter := &AsyncSubmitter{Store: store, Processor: processor, Workers: 1}
	recovered, err := submitter.Recover()
	if err != nil || recovered != 2 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	submitter.Close()
	processor.mu.Lock()
	jobs := append([]string(nil), processor.jobs...)
	processor.mu.Unlock()
	if len(jobs) != 2 || !containsString(jobs, "42/old1") || !containsString(jobs, "42/old2") {
		t.Fatalf("processed jobs=%v", jobs)
	}
	if names := regularFileNames(t, store.PendingDir); len(names) != 0 {
		t.Fatalf("recovered archives left pending: %v", names)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestFailedRawArchivesAreBounded(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxFailedRawArchives+5; index++ {
		path, err := store.SaveRaw([]byte("bad"), "42", "bad")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRaw(path, false); err != nil {
			t.Fatal(err)
		}
	}
	if names := regularFileNames(t, store.FailedDir); len(names) != MaxFailedRawArchives {
		t.Fatalf("got %d failed archives, want %d", len(names), MaxFailedRawArchives)
	}
	if names := regularFileNames(t, store.PendingDir); len(names) != 0 {
		t.Fatalf("failed archives left pending: %v", names)
	}
}

func TestFinishRawRejectsPathsOutsidePending(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(store.RawDir, "mysekai_42_x.bin")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRaw(outside, true); err == nil {
		t.Fatal("expected an error for a path outside the pending directory")
	}
}

func TestParseRawName(t *testing.T) {
	longTask := strings.Repeat("a", 64)
	cases := []struct {
		name   string
		user   string
		task   string
		wantOK bool
	}{
		{"mysekai_42_task1.bin", "42", "task1", true},
		{"mysekai_unknown_a_b.bin", "unknown", "a_b", true},
		{"mysekai_42_" + longTask + "_abcd1234.bin", "42", longTask, true},
		{"mysekai_x1_task.bin", "", "", false},
		{"other.bin", "", "", false},
		{"mysekai_42_task.txt", "", "", false},
	}
	for _, tc := range cases {
		user, task, ok := parseRawName(tc.name)
		if ok != tc.wantOK || user != tc.user || task != tc.task {
			t.Errorf("parseRawName(%q)=(%q,%q,%v), want (%q,%q,%v)", tc.name, user, task, ok, tc.user, tc.task, tc.wantOK)
		}
	}
}

func TestChunkStoreRemembersSubmittedUploadAfterCleanup(t *testing.T) {
	store, err := NewChunkStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, complete, err := store.Add("done", 0, 1, []byte("data")); err != nil || !complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	if err := store.MarkSubmitted("done"); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("done"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add("done", 0, 1, []byte("data")); err != ErrChunkAlreadySubmitted {
		t.Fatalf("got %v, want submitted marker after cleanup", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "done")); !os.IsNotExist(err) {
		t.Fatalf("retry recreated the chunk directory: %v", err)
	}
}

func TestChunkStorePrunesStaleUploadsAndMarkers(t *testing.T) {
	store, err := NewChunkStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"stale", "fresh"} {
		if _, _, err := store.Add(id, 0, 2, []byte("part")); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkSubmitted("old"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSubmitted("recent"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	staleTime := now.Add(-StaleUploadAge - time.Minute)
	if err := os.Chtimes(filepath.Join(store.Root, "stale"), staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-SubmittedRetention - time.Minute)
	if err := os.Chtimes(filepath.Join(store.Root, ".submitted", "old"), expired, expired); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneStale(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale upload remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "fresh", "chunk_0")); err != nil {
		t.Fatalf("fresh upload was pruned: %v", err)
	}
	if _, _, err := store.Add("old", 0, 1, []byte("data")); err != nil {
		t.Fatalf("expired marker still blocks the upload ID: %v", err)
	}
	if _, _, err := store.Add("recent", 0, 1, []byte("data")); err != ErrChunkAlreadySubmitted {
		t.Fatalf("got %v, want recent marker kept", err)
	}
}

func TestStoreKeepsSameTaskRawArchivesDistinct(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SaveRaw([]byte("first"), "42", "same-task")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SaveRaw([]byte("second"), "42", "same-task")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("raw paths collided: %q", first)
	}
	firstData, err := os.ReadFile(first)
	if err != nil || string(firstData) != "first" {
		t.Fatalf("first=%q err=%v", firstData, err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil || string(secondData) != "second" {
		t.Fatalf("second=%q err=%v", secondData, err)
	}
}

func TestStoreArchivesAndPromotesLatest(t *testing.T) {
	store, err := NewStore(testSettings(t))
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := os.WriteFile(filepath.Join(output, "site_5.png"), []byte("map"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := store.Archive(output, "42", time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(archive, filepath.Join("42", "20260817_010203")) {
		t.Fatalf("archive=%q", archive)
	}
	if err := store.PromoteLatest(archive); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(store.LatestDir, "site_5.png"))
	if err != nil || string(got) != "map" {
		t.Fatalf("latest=%q err=%v", got, err)
	}
}

type recordedNotifier struct {
	outputDir string
	taskID    string
	playerID  string
	imageBase string
}

func (n *recordedNotifier) Notify(_ context.Context, outputDir, taskID, playerID, imageBase string) error {
	n.outputDir, n.taskID, n.playerID, n.imageBase = outputDir, taskID, playerID, imageBase
	return nil
}

func TestGenerationProcessorCreatesArchiveAndNotifies(t *testing.T) {
	settings := testSettings(t)
	store, err := NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AES_KEY", "0123456789abcdef")
	t.Setenv("AES_IV", "fedcba9876543210")
	payload := map[string]any{
		"updatedResources": map[string]any{
			"userMysekaiHarvestMaps": []any{map[string]any{
				"mysekaiSiteId": 5,
				"userMysekaiSiteHarvestResourceDrops": []any{map[string]any{
					"resourceId": 5, "positionX": 1.0, "positionZ": 2.0,
				}},
			}},
		},
	}
	plain, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rawPath, err := store.SaveRaw(encryptForServiceTest(t, plain), "42", "task123")
	if err != nil {
		t.Fatal(err)
	}
	notifier := &recordedNotifier{}
	processor := NewGenerationProcessor(settings, store, notifier)
	processor.BarkImageBase = "https://cdn.example"
	processor.Now = func() time.Time { return time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC) }
	if err := processor.Process(context.Background(), rawPath, "42", "task123"); err != nil {
		t.Fatal(err)
	}
	if notifier.outputDir == "" || notifier.taskID != "task123" || notifier.playerID != "42" {
		t.Fatalf("notifier=%#v", notifier)
	}
	if !strings.Contains(notifier.imageBase, "/archive/by-id/42/20260817_010203") {
		t.Fatalf("image base=%q", notifier.imageBase)
	}
	if _, err := os.Stat(filepath.Join(notifier.outputDir, "site_5.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.LatestDir, "rare_resources.txt")); err != nil {
		t.Fatal(err)
	}
}

func encryptForServiceTest(t *testing.T, plain []byte) []byte {
	t.Helper()
	key := []byte("0123456789abcdef")
	iv := []byte("fedcba9876543210")
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(output, padded)
	return output
}
