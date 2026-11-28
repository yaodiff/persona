---
name: persona-assistant
description: Everyday personal assistant skills for Persona on HarmonyOS 7 (asking Persona questions, capturing to-dos, searching the personal knowledge base, and logging daily water intake).
version: 1.0.0
---

# Persona Assistant Skill

Use this skill when the user wants to interact with their personal Persona assistant in everyday language:

1. **Ask Persona (`askPersona`)**: Answer questions, summarize notes, or continue the user's main Persona conversation.
2. **Add To-Do (`addTodo`)**: Capture a new to-do item with title and optional notes.
3. **Search Knowledge (`searchKnowledge`)**: Look up personal notes, documents, projects, and meeting minutes.
4. **Log Water (`logWater`)**: Record a glass or specified millilitres of water in Persona Health.
5. **Start Meeting Recording (`startRecording`)**: Verify that the paired Hub allows meeting recording (failing closed when `module.recording` is off) and return the recording deep link.

All requests execute through Persona's authenticated Hub connection stored in the device Asset Store. Never expose device tokens or personal Hub addresses in outputs.
