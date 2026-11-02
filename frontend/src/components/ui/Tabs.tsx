import { cn } from "./cn"

export type TabItem = {
  value: string
  label: string
  count?: number
}

export type TabsProps = {
  items: TabItem[]
  value: string
  onValueChange: (value: string) => void
  className?: string
  ariaLabel?: string
}

/** Accessible, controlled tab strip. Panels stay with the feature component. */
export function Tabs({ items, value, onValueChange, className, ariaLabel }: TabsProps) {
  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      className={cn("inline-flex flex-wrap items-center gap-1 rounded-lg bg-muted p-1", className)}
    >
      {items.map((item) => {
        const selected = item.value === value
        return (
          <button
            key={item.value}
            type="button"
            role="tab"
            aria-selected={selected}
            onClick={() => onValueChange(item.value)}
            className={cn(
              "inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-xs font-medium transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              selected
                ? "bg-card text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {item.label}
            {typeof item.count === "number" ? (
              <span className="rounded bg-muted px-1 text-[10px] text-muted-foreground">
                {item.count}
              </span>
            ) : null}
          </button>
        )
      })}
    </div>
  )
}
