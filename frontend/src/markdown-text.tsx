import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import "./markdown.css";

export function MarkdownText({ content }: { content: string }) {
  return (
    <div className="markdown-text">
      <ReactMarkdown
        skipHtml
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[[rehypeHighlight, { detect: false }]]}
        components={{
          table: ({ node: _, ...props }) => (
            <div className="markdown-table">
              <table {...props} />
            </div>
          ),
          img: ({ alt }) => (alt ? <span>{alt}</span> : null),
        }}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
}
