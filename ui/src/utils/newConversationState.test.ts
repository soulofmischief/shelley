import { needsOptimisticWorkingState } from "./newConversationState";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

assert(
  needsOptimisticWorkingState([], "new-id"),
  "a conversation absent from the list needs optimistic working state",
);
assert(
  !needsOptimisticWorkingState([{ conversation_id: "new-id" }], "new-id"),
  "an authoritative list entry must win when it arrives before create returns",
);
assert(
  needsOptimisticWorkingState([{ conversation_id: "other-id" }], "new-id"),
  "an unrelated list entry does not initialize the new conversation",
);

console.log("newConversationState: 3 passed");
