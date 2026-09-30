//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"trpggame/internal/middleware"
	"trpggame/internal/model"
	"trpggame/internal/ws"
)

type memoryTestProcess struct {
	command *exec.Cmd
	logPath string
	address string
}

func (p *memoryTestProcess) stop() {
	if p == nil || p.command == nil || p.command.Process == nil {
		return
	}
	_ = p.command.Process.Kill()
	_ = p.command.Wait()
	p.command = nil
}

func memoryTestFreePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	return port
}

func memoryTestStartProcess(t *testing.T, binary, port, mysqlAddress, mysqlUser, mysqlPassword, mysqlDatabase, redisAddress, minioAddress string, pollMS int) *memoryTestProcess {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary)
	command.Env = append(os.Environ(),
		"TRPG_SERVER_PORT="+port,
		"TRPG_SERVER_MODE=test",
		"TRPG_DATABASE_HOST="+strings.Split(mysqlAddress, ":")[0],
		"TRPG_DATABASE_PORT="+strings.Split(mysqlAddress, ":")[1],
		"TRPG_DATABASE_USER="+mysqlUser,
		"TRPG_DATABASE_PASSWORD="+mysqlPassword,
		"TRPG_DATABASE_DBNAME="+mysqlDatabase,
		"TRPG_DATABASE_LOC=UTC",
		"TRPG_REDIS_ADDR="+redisAddress,
		"TRPG_MINIO_ENDPOINT="+minioAddress,
		"TRPG_MINIO_ACCESSKEY=test",
		"TRPG_MINIO_SECRETKEY=test-secret",
		"TRPG_MINIO_BUCKET=memory-process-test",
		"TRPG_JWT_SECRET=memory-process-test-secret",
		"TRPG_GAME_MEMORY_NEW_ROOMS_ENABLED=false",
		fmt.Sprintf("TRPG_GAME_ARCHIVE_POLL_INTERVAL_MS=%d", pollMS),
	)
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	_ = logFile.Close()
	process := &memoryTestProcess{command: command, logPath: logPath, address: "http://127.0.0.1:" + port}
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, requestErr := client.Get(process.address + "/api/openapi.yaml")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return process
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	process.stop()
	output, _ := os.ReadFile(logPath)
	t.Fatalf("server did not start: %s", output)
	return nil
}

func memoryTestRequest(t *testing.T, process *memoryTestProcess, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, process.address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if err = json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, decoded
}

func TestGameMemoryA6ProcessFailoverHTTPAndWebSocket(t *testing.T) {
	if os.Getenv("TRPG_TEST_MYSQL_DSN") == "" || os.Getenv("TRPG_TEST_REDIS_ADDR") == "" {
		t.Skip("dedicated MySQL and Redis are required")
	}
	parsed, err := mysqlDriver.ParseDSN(os.Getenv("TRPG_TEST_MYSQL_DSN"))
	if err != nil || parsed.Net != "tcp" {
		t.Fatalf("invalid test MySQL DSN: %v", err)
	}
	minio := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Query().Has("location") {
			writer.Header().Set("Content-Type", "application/xml")
			_, _ = writer.Write([]byte("<LocationConstraint>us-east-1</LocationConstraint>"))
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer minio.Close()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "memory-test-server")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binary, "./cmd/server")
	build.Dir = root
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build server: %v\n%s", buildErr, output)
	}
	minioAddress := strings.TrimPrefix(minio.URL, "http://")
	first := memoryTestStartProcess(t, binary, memoryTestFreePort(t), parsed.Addr, parsed.User, parsed.Passwd, parsed.DBName, os.Getenv("TRPG_TEST_REDIS_ADDR"), minioAddress, 60000)
	defer first.stop()
	// Let the first worker finish its initial empty scan before creating outbox data.
	time.Sleep(250 * time.Millisecond)
	f := newArchiveIntegrationFixture(t, "solo")
	characterID := uint(101)
	player := &model.RoomPlayer{RoomID: f.room.ID, UserID: 7, CharacterID: &characterID, Status: model.RoomPlayerStatusActive, IsReady: true, JoinedAt: time.Now().UTC()}
	if err = f.db.Create(player).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Where("room_id = ?", f.room.ID).Delete(&model.RoomPlayer{}) })
	token, err := middleware.GenerateToken(7, "owner", "memory-process-test-secret", 15)
	if err != nil {
		t.Fatal(err)
	}
	roomPath := fmt.Sprintf("/api/v1/games/%d/memory-status", f.room.ID)
	status, body := memoryTestRequest(t, first, http.MethodGet, roomPath, token, "")
	if status != http.StatusOK || body["data"].(map[string]any)["status"] != "pending" {
		t.Fatalf("first process pending status=%d body=%#v", status, body)
	}
	if status, _ := memoryTestRequest(t, first, http.MethodGet, roomPath, "", ""); status != http.StatusUnauthorized {
		t.Fatalf("memory status missing auth: %d", status)
	}
	otherToken, err := middleware.GenerateToken(8, "other", "memory-process-test-secret", 15)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := memoryTestRequest(t, first, http.MethodGet, roomPath, otherToken, ""); status != http.StatusNotFound {
		t.Fatalf("memory status leaked room: %d", status)
	}

	owner := uuid.NewString()
	if _, err = f.runtime.ClaimGameArchive(context.Background(), f.room.ID, owner, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	second := memoryTestStartProcess(t, binary, memoryTestFreePort(t), parsed.Addr, parsed.User, parsed.Passwd, parsed.DBName, os.Getenv("TRPG_TEST_REDIS_ADDR"), minioAddress, 250)
	defer second.stop()
	wsURL := "ws" + strings.TrimPrefix(second.address, "http") + "/ws?" + url.Values{"token": {token}, "room_id": {strconv.FormatUint(uint64(f.room.ID), 10)}}.Encode()
	connection, response, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": {"http://localhost:5173"}})
	if err != nil {
		responseCode := 0
		if response != nil {
			responseCode = response.StatusCode
		}
		t.Fatalf("connect second process websocket: status=%d err=%v", responseCode, err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var subscribed ws.Message
	if err = connection.ReadJSON(&subscribed); err != nil || subscribed.Type != ws.MsgSubscribed {
		t.Fatalf("second process subscription=%#v err=%v", subscribed, err)
	}
	first.stop()
	if _, err = f.runtime.ClaimGameArchive(context.Background(), f.room.ID, owner, 2*time.Second); err != nil {
		t.Fatalf("renew abandoned lease: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(12 * time.Second))
	for {
		var message ws.Message
		if err = connection.ReadJSON(&message); err != nil {
			t.Fatalf("missing archive status notification: %v", err)
		}
		if message.Type == ws.MsgMemoryStatusChanged {
			break
		}
	}
	status, body = memoryTestRequest(t, second, http.MethodGet, roomPath, token, "")
	if status != http.StatusOK || body["data"].(map[string]any)["status"] != "ready" {
		t.Fatalf("second process recovered status=%d body=%#v", status, body)
	}
	var recordCount int64
	if err = f.db.Model(&model.GameActionRecord{}).Where("room_id = ?", f.room.ID).Count(&recordCount).Error; err != nil || recordCount != 2 {
		t.Fatalf("archive count=%d err=%v", recordCount, err)
	}
	staleAction := fmt.Sprintf(`{"request_id":%q,"expected_turn":1,"expected_timeline_id":%q,"expected_generation":%q,"action_text":"look"}`, uuid.NewString(), uuid.NewString(), f.generation)
	status, body = memoryTestRequest(t, second, http.MethodPost, fmt.Sprintf("/api/v1/games/%d/action", f.room.ID), token, staleAction)
	if status != http.StatusConflict || body["code"] != float64(1342) {
		t.Fatalf("stale branch action status=%d body=%#v", status, body)
	}
}
