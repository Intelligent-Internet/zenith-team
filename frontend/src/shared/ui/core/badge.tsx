import * as React from 'react'
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'

import { cn } from './cn'

const badgeVariants = cva(
    'inline-flex items-center justify-center rounded-full border px-2 py-0.5 text-xs font-medium w-fit whitespace-nowrap shrink-0 [&>svg]:size-3 gap-1 [&>svg]:pointer-events-none transition-[color,box-shadow] overflow-hidden',
    {
        variants: {
            variant: {
                default: 'border-transparent bg-primary text-primary',
                secondary: 'border-transparent bg-secondary text-primary',
                destructive:
                    'border-transparent bg-red-2 text-white [a&]:hover:bg-red-2/90',
                outline:
                    'border-charcoal text-black dark:text-white [a&]:hover:bg-grey-3 dark:[a&]:hover:bg-state-hover-bg'
            }
        },
        defaultVariants: {
            variant: 'default'
        }
    }
)

function Badge({
    className,
    variant,
    asChild = false,
    ...props
}: React.ComponentProps<'span'> &
    VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
    const Comp = asChild ? Slot : 'span'

    return (
        <Comp
            data-slot="badge"
            className={cn(badgeVariants({ variant }), className)}
            {...props}
        />
    )
}

export { Badge, badgeVariants }
