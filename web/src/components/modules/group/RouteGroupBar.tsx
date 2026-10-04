'use client';

import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { DEFAULT_ROUTE_GROUP_ID, useCreateRouteGroup, useDeleteRouteGroup } from '@/api/endpoints/route-group';
import type { Group } from '@/api/endpoints/group';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogCancel } from '@/components/ui/alert-dialog';
import { toast } from '@/components/common/Toast';
import { cn } from '@/lib/utils';
import { useRouteGroupSelection } from './route-group-store';

export function RouteGroupBar({ groups }: { groups: Group[] }) {
    const t = useTranslations('routeGroup');
    const { data: routeGroups, activeID, select, isLoading, error } = useRouteGroupSelection();
    const createGroup = useCreateRouteGroup();
    const deleteGroup = useDeleteRouteGroup();
    const [createOpen, setCreateOpen] = useState(false);
    const [deleteOpen, setDeleteOpen] = useState(false);
    const [name, setName] = useState('');
    const activeGroup = routeGroups?.find((group) => group.id === activeID);

    return (
        <div className="shrink-0 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
                <div role="group" aria-label={t('title')} className="flex min-w-0 flex-1 gap-1.5 overflow-x-auto pb-1">
                    {(routeGroups ?? [{ id: DEFAULT_ROUTE_GROUP_ID, name: 'default' }]).map((routeGroup) => (
                        <button
                            key={routeGroup.id}
                            type="button"
                            aria-pressed={activeID === routeGroup.id}
                            onClick={() => select(routeGroup.id)}
                            className={cn(
                                'flex shrink-0 items-center gap-2 rounded-xl border px-3 py-2 text-sm transition-colors',
                                activeID === routeGroup.id
                                    ? 'border-primary/40 bg-primary/10 text-primary'
                                    : 'border-border bg-card text-muted-foreground hover:bg-muted',
                            )}
                        >
                            <span className="max-w-48 truncate" title={routeGroup.name}>{routeGroup.name}</span>
                            <span className="text-xs tabular-nums opacity-70">
                                {groups.filter((group) => (group.route_group_id ?? DEFAULT_ROUTE_GROUP_ID) === routeGroup.id).length}
                            </span>
                        </button>
                    ))}
                </div>
                <Button size="sm" variant="outline" disabled={isLoading || !!error} onClick={() => setCreateOpen(true)}>
                    <Plus className="size-4" />{t('create')}
                </Button>
                {activeID !== DEFAULT_ROUTE_GROUP_ID && (
                    <Button size="sm" variant="outline" disabled={!activeGroup} onClick={() => setDeleteOpen(true)}>
                        <Trash2 className="size-4" />{t('delete')}
                    </Button>
                )}
            </div>
            <p className="text-xs text-muted-foreground">{error ? error.message : t('hint')}</p>
            <Dialog open={createOpen} onOpenChange={setCreateOpen}>
                <DialogContent>
                    <DialogHeader>
                        <DialogTitle>{t('create')}</DialogTitle>
                        <DialogDescription>{t('hint')}</DialogDescription>
                    </DialogHeader>
                    <form className="space-y-4" onSubmit={(event) => {
                        event.preventDefault();
                        if (!name.trim() || createGroup.isPending) return;
                        createGroup.mutate(name.trim(), {
                            onSuccess: (created) => {
                                select(created.id);
                                setName('');
                                setCreateOpen(false);
                            },
                            onError: (failure) => toast.error(t('createFailed'), { description: failure.message }),
                        });
                    }}>
                        <label className="grid gap-2 text-sm">
                            {t('name')}
                            <Input value={name} onChange={(event) => setName(event.target.value)} maxLength={100} required disabled={createGroup.isPending} />
                        </label>
                        <div className="flex justify-end gap-2">
                            <Button type="button" variant="outline" onClick={() => setCreateOpen(false)}>{t('cancel')}</Button>
                            <Button type="submit" disabled={!name.trim() || createGroup.isPending}>{t('create')}</Button>
                        </div>
                    </form>
                </DialogContent>
            </Dialog>
            <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
                <AlertDialogContent>
                    <AlertDialogHeader>
                        <AlertDialogTitle>{t('delete')}</AlertDialogTitle>
                        <AlertDialogDescription>{t('deleteHint', { name: activeGroup?.name ?? '' })}</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                        <AlertDialogCancel disabled={deleteGroup.isPending}>{t('cancel')}</AlertDialogCancel>
                        <Button variant="destructive" disabled={deleteGroup.isPending || activeID === DEFAULT_ROUTE_GROUP_ID} onClick={() => {
                            deleteGroup.mutate(activeID, {
                                onSuccess: () => {
                                    select(DEFAULT_ROUTE_GROUP_ID);
                                    setDeleteOpen(false);
                                },
                                onError: (failure) => toast.error(t('deleteFailed'), { description: failure.message }),
                            });
                        }}>{t('delete')}</Button>
                    </AlertDialogFooter>
                </AlertDialogContent>
            </AlertDialog>
        </div>
    );
}
