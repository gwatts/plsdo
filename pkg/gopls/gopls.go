/*
Copyright © 2024 Gareth Watts <gareth@omnipotent.net>

Mostly written by an LLM.
*/
package gopls

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Match represents a reference to a symbol in the code.
type Match struct {
	URI            string
	Filename       string
	StartLine      int // 1-based indexing
	StartCharacter int // 1-based indexing
	EndLine        int // 1-based indexing
	EndCharacter   int // 1-based indexing
}

// GoplsClient encapsulates communication with the gopls server.
type GoplsClient struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	reader   *bufio.Reader
	writer   *bufio.Writer
	seq      int
	seqMutex sync.Mutex
}

// NewGoplsClient starts a gopls server and initializes the client.
func NewGoplsClient(projectRoot string) (*GoplsClient, error) {
	projectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("gopls", "-remote=auto")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	client := &GoplsClient{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		reader:   bufio.NewReader(stdout),
		writer:   bufio.NewWriter(stdin),
		seq:      0,
		seqMutex: sync.Mutex{},
	}

	// Initialize the LSP session
	if err := client.initialize(projectRoot); err != nil {
		return nil, err
	}

	return client, nil
}

// Close gracefully shuts down the gopls server.
func (c *GoplsClient) Close() error {
	// Send shutdown request
	shutdownRequestID := c.getSeq()
	shutdownRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      shutdownRequestID,
		"method":  "shutdown",
	}
	if err := c.sendMessage(shutdownRequest); err != nil {
		return err
	}

	// Wait for shutdown response
	for {
		resp, err := c.readMessage()
		if err != nil {
			return err
		}
		if id, ok := resp["id"]; ok && int(id.(float64)) == shutdownRequestID {
			break
		}
	}

	// Send exit notification
	exitNotification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "exit",
	}
	if err := c.sendMessage(exitNotification); err != nil {
		return err
	}

	// Close stdin and wait for the process to exit
	c.stdin.Close()
	return c.cmd.Wait()
}

// FindReferences finds all references to a symbol defined in a file at a given position.
func (c *GoplsClient) FindReferences(filename string, line, character int) ([]Match, error) {
	// Prepare and send the references request
	referencesRequestID := c.getSeq()
	referencesRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      referencesRequestID,
		"method":  "textDocument/references",
		"params": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"uri": pathToURI(filename),
			},
			"position": map[string]interface{}{
				"line":      line - 1,
				"character": character - 1,
			},
			"context": map[string]interface{}{
				"includeDeclaration": true,
			},
		},
	}
	if err := c.sendMessage(referencesRequest); err != nil {
		return nil, err
	}

	// Read responses and look for the references response
	for {
		resp, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		if id, ok := resp["id"]; ok && int(id.(float64)) == referencesRequestID {
			// Process and return the references
			matches, err := c.parseReferences(resp)
			if err != nil {
				return nil, err
			}
			return matches, nil
		}
	}
}

// initialize sets up the LSP session with gopls.
func (c *GoplsClient) initialize(projectRoot string) error {
	// Send Initialize request
	initializeRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      c.getSeq(),
		"method":  "initialize",
		"params": map[string]interface{}{
			"processId": nil,
			"rootUri":   pathToURI(projectRoot),
			"capabilities": map[string]interface{}{
				"textDocument": map[string]interface{}{
					"references": map[string]interface{}{},
				},
			},
		},
	}
	if err := c.sendMessage(initializeRequest); err != nil {
		return err
	}

	// Read Initialize response
	_, err := c.readMessage()
	if err != nil {
		return err
	}

	// Send Initialized notification
	initializedNotification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]interface{}{},
	}
	if err := c.sendMessage(initializedNotification); err != nil {
		return err
	}

	// Optionally, send DidChangeConfiguration
	didChangeConfigNotification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "workspace/didChangeConfiguration",
		"params": map[string]interface{}{
			"settings": map[string]interface{}{},
		},
	}
	return c.sendMessage(didChangeConfigNotification)
}

// getSeq generates a unique sequence ID for JSON-RPC messages.
func (c *GoplsClient) getSeq() int {
	c.seqMutex.Lock()
	defer c.seqMutex.Unlock()
	c.seq++
	return c.seq
}

// sendMessage sends a JSON-RPC message to gopls.
func (c *GoplsClient) sendMessage(msg map[string]interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := c.writer.WriteString(header); err != nil {
		return err
	}
	if _, err := c.writer.Write(data); err != nil {
		return err
	}
	return c.writer.Flush()
}

// readMessage reads a JSON-RPC message from gopls.
func (c *GoplsClient) readMessage() (map[string]interface{}, error) {
	// Read headers
	headers := make(map[string]string)
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid header line: %s", line)
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	contentLengthStr, ok := headers["Content-Length"]
	if !ok {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	contentLength, err := strconv.Atoi(contentLengthStr)
	if err != nil {
		return nil, fmt.Errorf("invalid Content-Length: %s", contentLengthStr)
	}
	// Read content
	content := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, content); err != nil {
		return nil, err
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(content, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// parseReferences processes the references response and returns a slice of Match structs.
func (c *GoplsClient) parseReferences(resp map[string]interface{}) ([]Match, error) {
	result, ok := resp["result"]
	if !ok {
		return nil, fmt.Errorf("no references found")
	}
	refs, ok := result.([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid references format")
	}
	var matches []Match
	for _, ref := range refs {
		refMap, ok := ref.(map[string]interface{})
		if !ok {
			continue
		}
		uri, ok := refMap["uri"].(string)
		if !ok {
			continue
		}
		rangeMap, ok := refMap["range"].(map[string]interface{})
		if !ok {
			continue
		}
		// Start position
		start, ok := rangeMap["start"].(map[string]interface{})
		if !ok {
			continue
		}
		startLine, ok := start["line"].(float64)
		if !ok {
			continue
		}
		startCharacter, ok := start["character"].(float64)
		if !ok {
			continue
		}
		// End position
		end, ok := rangeMap["end"].(map[string]interface{})
		if !ok {
			continue
		}
		endLine, ok := end["line"].(float64)
		if !ok {
			continue
		}
		endCharacter, ok := end["character"].(float64)
		if !ok {
			continue
		}
		match := Match{
			URI:            uri,
			Filename:       uriToPath(uri),
			StartLine:      int(startLine) + 1,      // Convert to 1-based indexing
			StartCharacter: int(startCharacter) + 1, // Convert to 1-based indexing
			EndLine:        int(endLine) + 1,        // Convert to 1-based indexing
			EndCharacter:   int(endCharacter) + 1,   // Convert to 1-based indexing
		}
		matches = append(matches, match)
	}
	return matches, nil
}

// pathToURI converts a file path to a URI.
func pathToURI(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func uriToPath(uri string) string {
	return strings.Replace(uri, "file://", "", 1)
}
