package bridge

func (s *Service) StoreMediaAttachment(event MessageEvent) (MessageEvent, error) {
	// Media bytes never become deployment state. Attachment metadata can remain
	// on the observation event, but raw content and local URLs are discarded.
	event.MediaBase64 = ""
	event.MediaURL = ""
	return event, nil
}
