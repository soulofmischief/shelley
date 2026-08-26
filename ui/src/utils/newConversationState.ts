interface ConversationState {
  conversation_id: string;
}

export function needsOptimisticWorkingState(
  conversations: ConversationState[],
  conversationId: string,
): boolean {
  return !conversations.some((conversation) => conversation.conversation_id === conversationId);
}
