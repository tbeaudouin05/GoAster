---
name: model-administration
description: Owner/operator-only workflow for adding an OpenAI model through a real GoAster prompt test.
---

# GoAster model administration

This is an owner/operator skill, not a Telegram command and not a public model
catalog. Never add a runtime `/add-model` command or claim that catalog
membership verifies a model.

Use this workflow only when the owner or operator asks to add or change a
configured OpenAI model:

1. Use the approved OpenAI catalog lookup only to discover a candidate exact
   model ID. Catalog lookup is discovery, never verification; do not describe
   the candidate as verified because it appears in the catalog.
2. Locate the real current configuration used by the running instance,
   normally `~/.goaster/artefacts/config/openai-models.json`. Save its complete bytes and keep
   the previous valid configuration available for restoration. Add the
   candidate to its `models` array with an explicit alias when wanted, and
   select its exact ID for the test. For the terminal, put the candidate first
   in `models`; for an agent chat, select its exact ID for that chat. Write a
   same-directory temporary JSON file, validate it, restrict its permissions,
   and atomically replace the real file. Do not edit the embedded template as a
   substitute for the current runtime file.
3. Without restarting GoAster, confirm the test uses the candidate's exact ID,
   then run a deterministic prompt through the real current GoAster binary and
   that real configuration. This is the only model verification. Use exactly
   this prompt:
   `Reply with exactly GOASTER_MODEL_TEST_OK and nothing else.` Verify the
   response is exactly `GOASTER_MODEL_TEST_OK`; a catalog result, a successful
   config parse, or an approval is not a prompt test.
4. Retain the candidate only after that real prompt succeeds, restoring any
   temporary changes to the default or chat selection if they were only for
   the test. If the prompt fails, times out, is not exact, or the result is uncertain, atomically
   restore the complete previous configuration and report that the candidate
   was not retained. Never leave a failed candidate in the live file.

GoAster reloads the JSON configuration during normal requests, so a successful
change takes effect without a restart. Keep per-chat selections as resolved
exact IDs; do not expose catalog lookup or model administration as a public
chat command.
