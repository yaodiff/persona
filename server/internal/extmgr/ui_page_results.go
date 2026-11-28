package extmgr

import (
	"encoding/json"
	"net/url"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/uistream"
)

// pageResult checks the presentation-only schema and the existing native gates.
// Keep original bytes: optional false/empty properties are not DTO serialization.
func pageResult(s *pageSession, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	} // older process bridges had no presentation result
	if err := uistream.ValidateActionResult(raw); err != nil {
		return nil, err
	}
	var result extension.ActionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if err := checkPushes(s.manifest, s.item, nil, result.Push); err != nil {
		return nil, err
	}
	if result.OpenURL != "" {
		target, err := url.Parse(result.OpenURL)
		if err != nil || target.User != nil {
			return nil, pageError("invalid")
		}
		if err := checkOpenURL(s.extension, s.manifest, s.item, "", result.OpenURL); err != nil {
			return nil, err
		}
	}
	if err := wire.CheckLines(result.Lines); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), raw...), nil
}

func pageReceiptFrame(request string, receipt *wire.UIStreamReceipt, result json.RawMessage) json.RawMessage {
	frame := streamFrame("receipt", request, receipt)
	if len(result) == 0 {
		return frame
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(frame, &envelope)
	var object map[string]json.RawMessage
	_ = json.Unmarshal(envelope["receipt"], &object)
	object["result"] = result
	envelope["receipt"], _ = json.Marshal(object)
	frame, _ = json.Marshal(envelope)
	return frame
}

func pageReplayFrame(frame json.RawMessage, request string) json.RawMessage {
	var reply wire.UIStreamServerEnvelope
	_ = json.Unmarshal(frame, &reply)
	var original map[string]json.RawMessage
	_ = json.Unmarshal(frame, &original)
	var receipt map[string]json.RawMessage
	_ = json.Unmarshal(original["receipt"], &receipt)
	return pageReceiptFrame(request, reply.Receipt, receipt["result"])
}
