package anigate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"` // always echoed; null when the request id is unknown (spec-required for parse errors)
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResult struct {
	Content           []toolContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

const maxStdioLineBytes = 10 * 1024 * 1024

func ServeStdio(r io.Reader, w io.Writer, svc *Service, log *slog.Logger) int {
	reader := bufio.NewReaderSize(r, 64*1024)
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	for {
		line, tooLong, err := readBoundedLine(reader, maxStdioLineBytes)
		if tooLong {
			// An oversized frame fails on its own; the server keeps serving
			// instead of dying (parity with the per-request HTTP behavior).
			resp := rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "request frame exceeds size limit"}}
			if werr := writeRPC(bw, resp); werr != nil {
				log.Error("write rpc", "err", werr)
				return 1
			}
		} else if len(line) > 0 {
			if resp, ok := dispatchJSON(line, svc); ok {
				if werr := writeRPC(bw, resp); werr != nil {
					log.Error("write rpc", "err", werr)
					return 1
				}
			}
		}
		if err == io.EOF {
			return 0
		}
		if err != nil {
			log.Error("read stdin", "err", err)
			return 1
		}
	}
}

// readBoundedLine returns the next newline-delimited frame without its
// trailing newline. Frames over limit are discarded in full and reported via
// tooLong; err is io.EOF once the input is exhausted.
func readBoundedLine(r *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, rerr := r.ReadSlice('\n')
		if !tooLong {
			buf = append(buf, chunk...)
			if len(buf) > limit {
				tooLong = true
				buf = nil
			}
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), tooLong, rerr
	}
}

func dispatchJSON(b []byte, svc *Service) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}, true
	}
	if req.ID == nil && isNotification(req.Method) {
		return rpcResponse{}, false
	}
	return dispatch(req, svc), true
}

func dispatch(req rpcRequest, svc *Service) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": negotiateProtocolVersion(req.Params),
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "anigate",
				"version": Version,
			},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": svc.Tools()}
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params"}
			return resp
		}
		result, err := svc.CallTool(params.Name, params.Arguments)
		tr := encodeToolResult(result, err)
		resp.Result = tr
	case "resources/list":
		resp.Result = map[string]any{"resources": []any{}}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": []any{}}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found"}
	}
	return resp
}

// negotiateProtocolVersion echoes the client's requested MCP protocol version
// when the server supports it, otherwise answers with the latest supported one.
func negotiateProtocolVersion(params json.RawMessage) string {
	const latest = "2025-06-18"
	supported := map[string]bool{"2024-11-05": true, "2025-03-26": true, latest: true}
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &p) == nil && supported[p.ProtocolVersion] {
		return p.ProtocolVersion
	}
	return latest
}

func encodeToolResult(result any, err error) toolResult {
	if err != nil {
		return toolResult{
			IsError: true,
			Content: []toolContent{{
				Type: "text",
				Text: err.Error(),
			}},
		}
	}
	b, jsonErr := json.MarshalIndent(result, "", "  ")
	if jsonErr != nil {
		return toolResult{
			IsError: true,
			Content: []toolContent{{
				Type: "text",
				Text: jsonErr.Error(),
			}},
		}
	}
	return toolResult{
		Content: []toolContent{{
			Type: "text",
			Text: string(b),
		}},
		StructuredContent: result,
	}
}

func writeRPC(w *bufio.Writer, resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, string(b)); err != nil {
		return err
	}
	return w.Flush()
}

func isNotification(method string) bool {
	return method == "notifications/initialized" || method == "notifications/cancelled" || method == "notifications/progress"
}
