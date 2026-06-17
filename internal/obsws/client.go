package obsws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	opHello           = 0
	opIdentify        = 1
	opIdentified      = 2
	opEvent           = 5
	opRequest         = 6
	opRequestResponse = 7

	wsText         = 1
	wsClose        = 8
	wsPing         = 9
	wsPong         = 10
	wsGUID         = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsMaxFrameSize = 1024 * 1024
)

var requestCounter uint64

type Client struct {
	URL      string
	Password string
	Timeout  time.Duration
}

type session struct {
	ws      *websocketConn
	timeout time.Duration
}

type obsMessage struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

type helloData struct {
	RPCVersion     int                `json:"rpcVersion"`
	Authentication *authenticationSet `json:"authentication"`
}

type authenticationSet struct {
	Challenge string `json:"challenge"`
	Salt      string `json:"salt"`
}

type identifyData struct {
	RPCVersion         int    `json:"rpcVersion"`
	Authentication     string `json:"authentication,omitempty"`
	EventSubscriptions int    `json:"eventSubscriptions"`
}

type responseData struct {
	RequestType   string          `json:"requestType"`
	RequestID     string          `json:"requestId"`
	RequestStatus requestStatus   `json:"requestStatus"`
	ResponseData  json.RawMessage `json:"responseData"`
}

type requestStatus struct {
	Result  bool   `json:"result"`
	Code    int    `json:"code"`
	Comment string `json:"comment"`
}

func (c Client) StartRecord() error {
	return c.call("StartRecord")
}

func (c Client) StopRecord() (string, error) {
	var output stopRecordData
	if err := c.callWithResponse("StopRecord", nil, &output); err != nil {
		return "", err
	}
	return output.OutputPath, nil
}

func (c Client) IsRecording() (bool, error) {
	var status recordStatusData
	if err := c.callWithResponse("GetRecordStatus", nil, &status); err != nil {
		return false, err
	}
	return status.OutputActive, nil
}

func (c Client) CurrentSceneScreenshot() ([]byte, error) {
	sceneName, err := c.CurrentSceneName()
	if err != nil {
		return nil, err
	}

	var screenshot sourceScreenshotData
	if err := c.callWithResponse("GetSourceScreenshot", sourceScreenshotRequest{
		SourceName:  sceneName,
		ImageFormat: "png",
		ImageWidth:  320,
		ImageHeight: 180,
	}, &screenshot); err != nil {
		return nil, err
	}

	return decodeImageDataURL(screenshot.ImageData)
}

func (c Client) CurrentSceneName() (string, error) {
	var scene currentProgramSceneData
	if err := c.callWithResponse("GetCurrentProgramScene", nil, &scene); err != nil {
		return "", err
	}
	if scene.CurrentProgramSceneName == "" {
		return "", errors.New("OBS did not return the current scene")
	}
	return scene.CurrentProgramSceneName, nil
}

func (c Client) CurrentWindowName() (string, error) {
	sceneName, err := c.CurrentSceneName()
	if err != nil {
		return "", err
	}

	var items sceneItemListData
	if err := c.callWithResponse("GetSceneItemList", sceneItemListRequest{
		SceneName: sceneName,
	}, &items); err != nil {
		return "", err
	}

	for _, item := range items.SceneItems {
		if !item.SceneItemEnabled || item.SourceName == "" {
			continue
		}

		var settings inputSettingsData
		if err := c.callWithResponse("GetInputSettings", inputSettingsRequest{
			InputName: item.SourceName,
		}, &settings); err != nil {
			continue
		}

		window, _ := settings.InputSettings["window"].(string)
		window = strings.TrimSpace(window)
		if window != "" {
			return window, nil
		}
	}

	return sceneName, nil
}

func (c Client) RecordDirectory() (string, error) {
	var data recordDirectoryData
	if err := c.callWithResponse("GetRecordDirectory", nil, &data); err != nil {
		return "", err
	}
	if data.RecordDirectory == "" {
		return "", errors.New("OBS did not return the recording directory")
	}
	return data.RecordDirectory, nil
}

type stopRecordData struct {
	OutputPath string `json:"outputPath"`
}

type recordStatusData struct {
	OutputActive bool `json:"outputActive"`
}

type currentProgramSceneData struct {
	CurrentProgramSceneName string `json:"currentProgramSceneName"`
}

type recordDirectoryData struct {
	RecordDirectory string `json:"recordDirectory"`
}

type sceneItemListRequest struct {
	SceneName string `json:"sceneName"`
}

type sceneItemListData struct {
	SceneItems []sceneItemData `json:"sceneItems"`
}

type sceneItemData struct {
	SourceName       string `json:"sourceName"`
	SceneItemEnabled bool   `json:"sceneItemEnabled"`
}

type inputSettingsRequest struct {
	InputName string `json:"inputName"`
}

type inputSettingsData struct {
	InputSettings map[string]any `json:"inputSettings"`
}

type sourceScreenshotRequest struct {
	SourceName  string `json:"sourceName"`
	ImageFormat string `json:"imageFormat"`
	ImageWidth  int    `json:"imageWidth,omitempty"`
	ImageHeight int    `json:"imageHeight,omitempty"`
}

type sourceScreenshotData struct {
	ImageData string `json:"imageData"`
}

func (c Client) call(requestType string) error {
	return c.callWithResponse(requestType, nil, nil)
}

func (c Client) callWithResponse(requestType string, requestPayload any, responsePayload any) error {
	s, err := c.connect()
	if err != nil {
		return err
	}
	defer s.close()

	requestID := nextRequestID()
	request := struct {
		RequestType string `json:"requestType"`
		RequestID   string `json:"requestId"`
		RequestData any    `json:"requestData,omitempty"`
	}{
		RequestType: requestType,
		RequestID:   requestID,
		RequestData: requestPayload,
	}
	if err := s.send(opRequest, request); err != nil {
		return err
	}

	for {
		var msg obsMessage
		if err := s.read(&msg); err != nil {
			return err
		}
		if msg.Op == opEvent {
			continue
		}
		if msg.Op != opRequestResponse {
			continue
		}

		var response responseData
		if err := json.Unmarshal(msg.D, &response); err != nil {
			return fmt.Errorf("OBS returned an invalid response: %w", err)
		}
		if response.RequestID != requestID {
			continue
		}
		if !response.RequestStatus.Result {
			if response.RequestStatus.Comment != "" {
				return fmt.Errorf("OBS could not execute %s: %s (code %d)", requestType, response.RequestStatus.Comment, response.RequestStatus.Code)
			}
			return fmt.Errorf("OBS could not execute %s (code %d)", requestType, response.RequestStatus.Code)
		}
		if responsePayload != nil && len(response.ResponseData) > 0 {
			if err := json.Unmarshal(response.ResponseData, responsePayload); err != nil {
				return fmt.Errorf("OBS returned invalid responseData for %s: %w", requestType, err)
			}
		}
		return nil
	}
}

func (c Client) connect() (*session, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ws, err := dialWebsocket(c.URL, timeout)
	if err != nil {
		return nil, err
	}

	s := &session{ws: ws, timeout: timeout}
	var hello obsMessage
	if err := s.read(&hello); err != nil {
		ws.close()
		return nil, err
	}
	if hello.Op != opHello {
		ws.close()
		return nil, fmt.Errorf("OBS returned an unexpected message on connect: op %d", hello.Op)
	}

	var data helloData
	if err := json.Unmarshal(hello.D, &data); err != nil {
		ws.close()
		return nil, fmt.Errorf("OBS returned an invalid Hello message: %w", err)
	}
	if data.RPCVersion < 1 {
		ws.close()
		return nil, fmt.Errorf("OBS WebSocket uses rpcVersion %d, but 1 is required", data.RPCVersion)
	}

	identify := identifyData{
		RPCVersion:         1,
		EventSubscriptions: 0,
	}
	if data.Authentication != nil {
		if c.Password == "" {
			ws.close()
			return nil, errors.New("OBS WebSocket requires a password; configure obs_websocket.password in config.json")
		}
		identify.Authentication = createAuthentication(c.Password, data.Authentication.Salt, data.Authentication.Challenge)
	}

	if err := s.send(opIdentify, identify); err != nil {
		ws.close()
		return nil, err
	}

	for {
		var identified obsMessage
		if err := s.read(&identified); err != nil {
			ws.close()
			return nil, err
		}
		if identified.Op == opEvent {
			continue
		}
		if identified.Op != opIdentified {
			ws.close()
			return nil, fmt.Errorf("OBS did not accept identification: op %d", identified.Op)
		}
		return s, nil
	}
}

func (s *session) send(op int, data any) error {
	payload, err := json.Marshal(struct {
		Op int `json:"op"`
		D  any `json:"d"`
	}{
		Op: op,
		D:  data,
	})
	if err != nil {
		return err
	}
	return s.ws.writeText(payload, s.timeout)
}

func (s *session) read(target any) error {
	payload, err := s.ws.readMessage(s.timeout)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("OBS returned invalid JSON: %w", err)
	}
	return nil
}

func (s *session) close() {
	s.ws.close()
}

func createAuthentication(password, salt, challenge string) string {
	secretHash := sha256.Sum256([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(secretHash[:])
	authHash := sha256.Sum256([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(authHash[:])
}

func nextRequestID() string {
	n := atomic.AddUint64(&requestCounter, 1)
	return fmt.Sprintf("obs-pktm-%d-%d", time.Now().UnixNano(), n)
}

type websocketConn struct {
	conn   net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}

func dialWebsocket(rawURL string, timeout time.Duration) (*websocketConn, error) {
	u, err := normalizeURL(rawURL)
	if err != nil {
		return nil, err
	}

	address := u.Host
	if !strings.Contains(address, ":") {
		if u.Scheme == "wss" {
			address += ":443"
		} else {
			address += ":80"
		}
	}

	var conn net.Conn
	if u.Scheme == "wss" {
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout}}
		conn, err = dialer.Dial("tcp", address)
	} else {
		conn, err = net.DialTimeout("tcp", address, timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("could not connect to OBS WebSocket at %s: %w", u.String(), err)
	}

	ws := &websocketConn{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}
	if err := ws.handshake(u, timeout); err != nil {
		conn.Close()
		return nil, err
	}
	return ws, nil
}

func normalizeURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		rawURL = "ws://localhost:4455"
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "ws://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("URL de OBS WebSocket invalida: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("URL de OBS WebSocket debe usar ws:// o wss://: %s", rawURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("URL de OBS WebSocket sin host: %s", rawURL)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func (ws *websocketConn) handshake(u *url.URL, timeout time.Duration) error {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: obswebsocket.json\r\n\r\n",
		path,
		u.Host,
		key,
	)

	if err := ws.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if _, err := io.WriteString(ws.conn, request); err != nil {
		return fmt.Errorf("fallo el handshake con OBS WebSocket: %w", err)
	}

	response, err := http.ReadResponse(ws.reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return fmt.Errorf("OBS WebSocket no respondio al handshake: %w", err)
	}
	if response.Body != nil {
		response.Body.Close()
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("OBS WebSocket rejected the handshake: %s", response.Status)
	}

	expectedAccept := websocketAccept(key)
	if response.Header.Get("Sec-WebSocket-Accept") != expectedAccept {
		return errors.New("OBS WebSocket returned an invalid Sec-WebSocket-Accept header")
	}
	_ = ws.conn.SetDeadline(time.Time{})
	return nil
}

func websocketAccept(key string) string {
	hash := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(hash[:])
}

func decodeImageDataURL(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("OBS returned an empty screenshot")
	}
	if i := strings.Index(value, ","); i >= 0 {
		value = value[i+1:]
	}

	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("OBS returned a screenshot with invalid base64 data: %w", err)
	}
	return data, nil
}

func (ws *websocketConn) readMessage(timeout time.Duration) ([]byte, error) {
	var payload []byte

	for {
		opcode, fin, data, err := ws.readFrame(timeout)
		if err != nil {
			return nil, err
		}

		switch opcode {
		case wsText, 0:
			payload = append(payload, data...)
			if fin {
				return payload, nil
			}
		case wsPing:
			_ = ws.writeFrame(wsPong, data, timeout)
		case wsPong:
			continue
		case wsClose:
			return nil, errors.New("OBS WebSocket cerro la conexion")
		default:
			if fin {
				continue
			}
		}
	}
}

func (ws *websocketConn) readFrame(timeout time.Duration) (byte, bool, []byte, error) {
	if err := ws.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, false, nil, err
	}

	header := make([]byte, 2)
	if _, err := io.ReadFull(ws.reader, header); err != nil {
		return 0, false, nil, fmt.Errorf("could not read from OBS WebSocket: %w", err)
	}

	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)

	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err := io.ReadFull(ws.reader, extended); err != nil {
			return 0, false, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err := io.ReadFull(ws.reader, extended); err != nil {
			return 0, false, nil, err
		}
		length = binary.BigEndian.Uint64(extended)
	}
	if length > wsMaxFrameSize {
		return 0, false, nil, fmt.Errorf("OBS returned a frame that is too large: %d bytes", length)
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(ws.reader, mask[:]); err != nil {
			return 0, false, nil, err
		}
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(ws.reader, data); err != nil {
		return 0, false, nil, err
	}
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return opcode, fin, data, nil
}

func (ws *websocketConn) writeText(payload []byte, timeout time.Duration) error {
	return ws.writeFrame(wsText, payload, timeout)
}

func (ws *websocketConn) writeFrame(opcode byte, payload []byte, timeout time.Duration) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if err := ws.conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}

	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xffff:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length))
		header = append(header, extended[:]...)
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)

	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}

	if _, err := ws.conn.Write(header); err != nil {
		return fmt.Errorf("could not write to OBS WebSocket: %w", err)
	}
	if _, err := ws.conn.Write(masked); err != nil {
		return fmt.Errorf("could not write to OBS WebSocket: %w", err)
	}
	return nil
}

func (ws *websocketConn) close() {
	_ = ws.writeFrame(wsClose, nil, time.Second)
	_ = ws.conn.Close()
}
