---
name: model-administration
description: Owner/operator workflow for changing configured OpenAI models and testing them through GoAster.
---

# GoAster model administration

Use this skill only when the owner or operator asks to add or change a
configured OpenAI model. Do not expose model administration as a public
chat command.

1. Read the running instance's configuration, normally
   `~/.goaster/artefacts/config/openai-models.json`, and save its complete
   contents for restoration.
2. Add the exact model ID provided by the owner or operator to `models`,
   with an alias if requested. Select its exact ID for the test in the
   relevant agent or Telegram chat. Use file or shell tools to write a
   same-directory temporary JSON file, validate it, restrict its permissions,
   and atomically replace the runtime file. Do not substitute the embedded
   template for the runtime file.
3. Without restarting GoAster, confirm the test uses the candidate's exact
   ID. Run this prompt through the real current GoAster agent and runtime
   configuration:

   `Reply with exactly GOASTER_MODEL_TEST_OK and nothing else.`

   The response must be exactly `GOASTER_MODEL_TEST_OK`. A valid JSON file
   or an approval does not verify the model.
4. If the test succeeds, keep the candidate and restore any chat selection
   changed only for testing. If it fails, times out, returns a different
   response, or is uncertain, atomically restore the complete previous
   configuration and report that the candidate was not retained.

GoAster reloads the JSON during normal requests, so no restart is needed.
Keep per-chat selections as resolved exact IDs.
