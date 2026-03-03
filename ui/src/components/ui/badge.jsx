import * as React from 'react'

import { cn } from '../../lib/utils'

const badgeVariants = {
  base: 'inline-flex items-center rounded-full border border-border px-3 py-1 text-xs font-semibold text-foreground',
  variants: {
    default: 'bg-accent text-accent-foreground',
    outline: 'bg-transparent',
  },
}

function Badge({ className, variant = 'default', ...props }) {
  return <span className={cn(badgeVariants.base, badgeVariants.variants[variant], className)} {...props} />
}

export { Badge }
