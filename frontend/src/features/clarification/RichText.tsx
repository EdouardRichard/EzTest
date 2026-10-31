"use client"

import Markdown from "react-markdown"
import remarkGfm from "remark-gfm"

import { cn } from "@/components/ui/cn"

export type RichTextProps = {
  children: string
  className?: string
}

/**
 * Renders assistant/user Markdown with GFM tables, strikethrough and task
 * lists. Raw HTML is deliberately not enabled (`rehype-raw` is not used), so
 * `<script>` input stays inert text and links get safe external attributes.
 */
export function RichText({ children, className }: RichTextProps) {
  return (
    <div
      data-testid="rich-text"
      className={cn("space-y-2 text-sm leading-relaxed text-foreground", className)}
    >
      <Markdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ node: _node, ...props }) => (
            <a
              {...props}
              target="_blank"
              rel="noopener noreferrer nofollow"
              className="text-primary underline underline-offset-2"
            />
          ),
          p: ({ node: _node, ...props }) => <p {...props} className="whitespace-pre-wrap" />,
          ul: ({ node: _node, ...props }) => <ul {...props} className="list-disc space-y-1 pl-5" />,
          ol: ({ node: _node, ...props }) => (
            <ol {...props} className="list-decimal space-y-1 pl-5" />
          ),
          li: ({ node: _node, ...props }) => <li {...props} className="leading-relaxed" />,
          table: ({ node: _node, ...props }) => (
            <div className="overflow-x-auto">
              <table {...props} className="w-full border-collapse text-xs" />
            </div>
          ),
          th: ({ node: _node, ...props }) => (
            <th
              {...props}
              className="border border-border bg-muted px-2 py-1 text-left font-semibold"
            />
          ),
          td: ({ node: _node, ...props }) => (
            <td {...props} className="border border-border px-2 py-1" />
          ),
          code: ({ node: _node, ...props }) => (
            <code {...props} className="rounded bg-muted px-1 py-0.5 font-mono text-[11px]" />
          ),
          pre: ({ node: _node, ...props }) => (
            <pre
              {...props}
              className="overflow-x-auto rounded-md bg-muted p-2 text-[11px] leading-relaxed"
            />
          ),
          blockquote: ({ node: _node, ...props }) => (
            <blockquote
              {...props}
              className="border-l-2 border-border pl-3 text-muted-foreground"
            />
          ),
          h1: ({ node: _node, ...props }) => <h3 {...props} className="text-base font-semibold" />,
          h2: ({ node: _node, ...props }) => <h4 {...props} className="text-sm font-semibold" />,
          h3: ({ node: _node, ...props }) => <h5 {...props} className="text-sm font-semibold" />,
        }}
      >
        {children}
      </Markdown>
    </div>
  )
}
