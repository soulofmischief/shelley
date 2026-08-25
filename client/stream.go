package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type streamMode struct {
	onlyAfterLastUser bool
	stopAtEndOfTurn   bool
}

type streamRenderer struct {
	output outputConfig
	mode   streamMode

	replay            []messageWire
	live              bool
	seenMessages      map[int64]bool
	hasDelta          bool
	lastDeltaSequence int64
	streamedText      bool
	streamedThinking  bool
}

func newStreamRenderer(output outputConfig, mode streamMode) *streamRenderer {
	return &streamRenderer{
		output:       output,
		mode:         mode,
		seenMessages: make(map[int64]bool),
	}
}

func (r *streamRenderer) consume(response streamResponseWire) (bool, error) {
	if !r.live {
		r.replay = append(r.replay, response.Messages...)
		if !response.SnapshotComplete {
			return false, nil
		}
		r.live = true
		return r.consumeReplay()
	}

	if delta := response.StreamDelta; delta != nil {
		if err := r.consumeDelta(*delta); err != nil {
			return false, err
		}
	}
	for _, message := range response.Messages {
		finished, err := r.consumeMessage(message)
		if err != nil {
			return false, err
		}
		if finished {
			return true, nil
		}
	}
	return false, nil
}

func (r *streamRenderer) consumeReplay() (bool, error) {
	var lastUserSequence int64
	for _, message := range r.replay {
		if message.Type == "user" && message.SequenceID > lastUserSequence {
			lastUserSequence = message.SequenceID
		}
	}

	var turnFinished bool
	for _, message := range r.replay {
		if r.mode.onlyAfterLastUser && message.SequenceID <= lastUserSequence {
			r.seenMessages[message.SequenceID] = true
			continue
		}
		finished, err := r.consumeMessage(message)
		if err != nil {
			return false, err
		}
		if finished && message.SequenceID > lastUserSequence {
			turnFinished = true
		}
	}
	r.replay = nil
	return turnFinished, nil
}

func (r *streamRenderer) consumeDelta(delta streamDeltaWire) error {
	if r.hasDelta {
		if delta.Seq == r.lastDeltaSequence {
			return nil
		}
		if delta.Seq != r.lastDeltaSequence+1 {
			return fmt.Errorf("stream delta sequence jumped from %d to %d", r.lastDeltaSequence, delta.Seq)
		}
	}
	r.hasDelta = true
	r.lastDeltaSequence = delta.Seq
	if r.output.jsonLines {
		return nil
	}

	switch delta.Type {
	case "thinking":
		r.streamedThinking = true
		_, err := fmt.Fprint(r.output.writer, r.output.dim(delta.Text))
		return err
	case "text":
		r.streamedText = true
		_, err := fmt.Fprint(r.output.writer, delta.Text)
		return err
	case "tool_input":
		return nil
	default:
		return fmt.Errorf("unknown stream delta type %q", delta.Type)
	}
}

func (r *streamRenderer) consumeMessage(message messageWire) (bool, error) {
	if r.seenMessages[message.SequenceID] {
		return false, nil
	}
	r.seenMessages[message.SequenceID] = true

	if r.output.jsonLines {
		if err := writeJSONLine(r.output.writer, simplifyMessage(message)); err != nil {
			return false, err
		}
	} else if err := r.output.printMessage(message, !r.streamedText, !r.streamedThinking); err != nil {
		return false, err
	}

	finished := r.mode.stopAtEndOfTurn && isEndOfTurn(message)
	if message.Type == "agent" || message.Type == "error" {
		r.streamedText = false
		r.streamedThinking = false
	}
	return finished, nil
}

func isEndOfTurn(message messageWire) bool {
	return (message.Type == "agent" || message.Type == "error") &&
		message.EndOfTurn != nil && *message.EndOfTurn
}

func streamConversation(cc *clientConfig, client *http.Client, baseURL, conversationID string, mode streamMode) error {
	request, err := cc.newRequest(http.MethodGet, baseURL+"/api/conversation/"+conversationID+"/stream", nil)
	if err != nil {
		return fmt.Errorf("create stream request: %w", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError("stream conversation", response)
	}

	renderer := newStreamRenderer(cc.output, mode)
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame streamResponseWire
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			return fmt.Errorf("decode stream frame: %w", err)
		}
		if frame.Heartbeat {
			continue
		}
		finished, err := renderer.consume(frame)
		if err != nil {
			return err
		}
		if finished {
			if !cc.output.jsonLines {
				if _, err := fmt.Fprintln(cc.output.writer); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	if !renderer.live {
		return fmt.Errorf("stream ended before snapshot completion")
	}
	if mode.stopAtEndOfTurn {
		return fmt.Errorf("stream ended before the agent turn completed")
	}
	return nil
}
