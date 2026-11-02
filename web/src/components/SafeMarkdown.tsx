import type { ComponentProps } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export function SafeMarkdown({ children, className }: { children: string; className?: string }) {
  return <div className={className}><ReactMarkdown
    remarkPlugins={[remarkGfm]}
    skipHtml
    components={{
      img: ({ alt }) => <span className="blocked-remote-image">Image blocked{alt ? `: ${alt}` : ""}</span>,
      a: (props: ComponentProps<"a">) => <a {...props} rel="noreferrer noopener" target="_blank" />,
    }}
  >{children}</ReactMarkdown></div>;
}
