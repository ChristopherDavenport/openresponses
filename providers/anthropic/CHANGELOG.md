# Changelog

## Unreleased

- `New` over the SDK's `MessageService`, `Create` over `CreateStream`,
  `WithMaxTokens` and `WithContinuations`; compaction is unsupported.
- Request encoding: instructions and system messages into the system
  prompt, items folded into alternating messages, tool use and results,
  thinking blocks with signatures, images and documents including Files
  API ids, function tools, `anthropic.*` server tools, tool choice,
  sampling, effort and thinking display, structured output and prompt
  caching. Fields with no Messages API equivalent are rejected with
  `unsupported_parameter`.
- Response streaming: text, thinking, tool use, extension blocks,
  citations, usage, stop reasons including refusals and resumed
  `pause_turn` turns, and upstream errors with their headers.
