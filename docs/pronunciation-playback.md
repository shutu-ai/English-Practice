# Pronunciation playback

V2.1.1 uses the browser's native `SpeechSynthesis` API. There is no cloud TTS,
API key, audio file, microphone input, speech-to-text, pronunciation score, or
automatic playback after submit.

The serving bundle keeps playback in `web/dist/speech.js`; the Vue source has
the matching `src/services/speech.ts` service. The service exposes `speak`,
`stop`, `isSupported`, voice discovery, selected voice, and rate. It trims
leading/trailing whitespace, reads the live input value, limits playback to
1000 characters, cancels existing speech before a new utterance, prefers an
explicit voice then English/en-US, and persists voice/rate in localStorage.

Playback is available for the current input, Your answer, More natural,
Suggested answer, and Alternative when the field exists and is non-empty.
Chinese prompts and explanations are never sent to speech. Playback does not
create attempts or change mastery, retention, difficulty, review, or
calibration state. Unsupported browsers keep Practice usable and show a
non-blocking support message in Settings.

The Speech Rate setting supports 0.8, 0.9, 1.0, 1.1, and 1.2. Voice lists
may arrive asynchronously; the UI listens for `voiceschanged` and falls back
to Auto/English/browser default when a saved voice is unavailable.
