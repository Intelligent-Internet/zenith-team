import * as React from 'react'
import * as ProgressPrimitive from '@radix-ui/react-progress'

import { cn } from './cn'

function Progress({
    className,
    value,
    ...props
}: React.ComponentProps<typeof ProgressPrimitive.Root>) {
    return (
        <ProgressPrimitive.Root
            data-slot="progress"
            className={cn(
                'bg-brand-primary/10 bg-brand-primary/10 relative h-2 w-full overflow-hidden rounded-full',
                className
            )}
            {...props}
        >
            <ProgressPrimitive.Indicator
                data-slot="progress-indicator"
                className="h-full w-full flex-1 transition-all rounded-xl"
                style={{ transform: `translateX(-${100 - (value || 0)}%)` }}
            />
        </ProgressPrimitive.Root>
    )
}

export { Progress }
