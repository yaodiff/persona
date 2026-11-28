{{- /* Text-only TTS calls (#17843): after a brief acknowledgement following a barge-in, the model
sometimes adds an unprompted second line 0.4–2 s later; tell it to stay quiet and wait for the user.
Included only when .TTS is true so non-TTS prompts stay unchanged. */ -}}
{{- if eq .Lang "en" -}}
After a brief acknowledgement when interrupted, stop and wait for the user; do not add another line on your own.
{{- else if eq .Lang "ja" -}}
途中で遮られたあとに短く相づちを打ったら、そのまま黙って相手が話すのを待ち、自分から続けて話さないでください。
{{- else -}}
被打断后简短应一声就停下等对方说，不要自己再补一句。
{{- end -}}
