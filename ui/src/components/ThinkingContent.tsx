import React, { useState } from "react";

interface ThinkingContentProps {
  thinking: string;
  summary?: string;
}

function ThinkingContent({ thinking, summary }: ThinkingContentProps) {
  const [isExpanded, setIsExpanded] = useState(true);

  const collapsedText = summary || thinking.split("\n")[0] || thinking;

  return (
    <div
      className="thinking-content"
      data-testid="thinking-content"
      style={{
        marginBottom: "0.5rem",
      }}
    >
      <div
        onClick={() => setIsExpanded(!isExpanded)}
        style={{
          cursor: "pointer",
          display: "flex",
          alignItems: "flex-start",
          gap: "0.5rem",
          marginLeft: 0,
        }}
      >
        <span style={{ flexShrink: 0 }}>💭</span>
        <div
          style={{
            flex: 1,
            fontStyle: "italic",
            color: "var(--text-secondary)",
            whiteSpace: "pre-wrap",
            wordBreak: "break-word",
          }}
        >
          {isExpanded ? thinking : collapsedText}
        </div>
        <button
          className="thinking-toggle"
          aria-label={isExpanded ? "Collapse" : "Expand"}
          aria-expanded={isExpanded}
          style={{
            background: "none",
            border: "none",
            padding: "0.25rem",
            cursor: "pointer",
            color: "var(--text-tertiary)",
            display: "flex",
            alignItems: "center",
            flexShrink: 0,
          }}
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            style={{
              transform: isExpanded ? "rotate(90deg)" : "rotate(0deg)",
              transition: "transform 0.2s",
            }}
          >
            <path
              d="M4.5 3L7.5 6L4.5 9"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </button>
      </div>
    </div>
  );
}

export default ThinkingContent;
