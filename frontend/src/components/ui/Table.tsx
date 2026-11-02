import type { ReactNode } from "react"

import { cn } from "./cn"

export type TableColumn<T> = {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  className?: string
}

export type TableProps<T> = {
  columns: TableColumn<T>[]
  rows: T[]
  rowKey: (row: T) => string
  ariaLabel?: string
  empty?: ReactNode
  className?: string
}

/** Small data table with a built-in empty row; feature pages own filtering. */
export function Table<T>({ columns, rows, rowKey, ariaLabel, empty, className }: TableProps<T>) {
  return (
    <div className={cn("w-full overflow-x-auto rounded-lg border border-border", className)}>
      <table aria-label={ariaLabel} className="w-full border-collapse text-sm">
        <thead className="bg-muted/70">
          <tr>
            {columns.map((column) => (
              <th
                key={column.key}
                scope="col"
                className={cn(
                  "px-3 py-2.5 text-left text-xs font-semibold text-muted-foreground",
                  column.className,
                )}
              >
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td
                colSpan={columns.length}
                className="px-3 py-8 text-center text-xs text-muted-foreground"
              >
                {empty ?? "暂无数据"}
              </td>
            </tr>
          ) : (
            rows.map((row) => (
              <tr key={rowKey(row)} className="border-t border-border hover:bg-muted/40">
                {columns.map((column) => (
                  <td key={column.key} className={cn("px-3 py-2.5 align-middle", column.className)}>
                    {column.render(row)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  )
}
