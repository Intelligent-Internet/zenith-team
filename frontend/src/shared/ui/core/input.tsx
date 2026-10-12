import * as React from 'react'

import { cn } from './cn'

function Input({ className, type, ...props }: React.ComponentProps<'input'>) {
    return (
        <input
            type={type}
            data-slot="input"
            className={cn(
                'file:text-primary placeholder:text-input-placeholder selection:bg-primary bg-[#f8fafb] dark:bg-[#A6FFFF1A] border-input text-black dark:text-white flex h-11 w-full min-w-0 rounded-md border px-3 py-1 transition-[color,box-shadow] outline-none file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 text-sm',
                className
            )}
            {...props}
        />
    )
}

export { Input }
