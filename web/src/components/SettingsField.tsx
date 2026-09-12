import { useId, type ReactNode } from 'react'
import { Lock } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

interface Props {
  label: string
  /** The variable or flag holding this field, when one does. */
  lockedBy?: string
  hint?: ReactNode
  children: (id: string, disabled: boolean) => ReactNode
}

/**
 * One labelled setting, with the honesty about whether it can be changed.
 *
 * A MAILMAN_* variable or a command-line flag sits above the config file, so
 * editing such a field here would save something that never takes effect. The
 * server refuses those saves outright; this is the half that stops anyone
 * reaching them in the first place, and says what to do instead.
 */
export function SettingsField({ label, lockedBy, hint, children }: Props) {
  const id = useId()

  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <Label htmlFor={id} className="text-xs text-muted-foreground">
          {label}
        </Label>
        {lockedBy && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Badge variant="outline" className="h-4 gap-1 px-1.5 text-[11px] font-normal">
                <Lock aria-hidden />
                {lockedBy}
              </Badge>
            </TooltipTrigger>
            <TooltipContent>
              {lockedBy.startsWith('-')
                ? `The ${lockedBy} flag sits above the config file, so changing this here would have no effect. Restart Mailman without it to edit this.`
                : `${lockedBy} sits above the config file, so changing this here would have no effect. Unset it and restart to edit this.`}
            </TooltipContent>
          </Tooltip>
        )}
      </div>

      {children(id, Boolean(lockedBy))}

      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}
