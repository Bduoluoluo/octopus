'use client';

import { useMemo, useState } from 'react';
import { BookMarked, Plus, Sparkles, Waypoints } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { GroupCard } from './Card';
import { PublicModelDialog } from './PublicModelDialog';
import { useGroupList } from '@/api/endpoints/group';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';
import { Button } from '@/components/ui/button';
import { MorphingDialog, MorphingDialogContainer, MorphingDialogContent } from '@/components/ui/morphing-dialog';
import { CreateDialogContent as GroupCreateContent } from '@/components/modules/group/Create';
import { GroupAutoGroupDialogContent } from '@/components/modules/group/AutoGroupDialog';
import { DEFAULT_ROUTE_GROUP_ID } from '@/api/endpoints/route-group';
import { RouteGroupBar } from './RouteGroupBar';
import { useRouteGroupSelection } from './route-group-store';

export function Group() {
    const { data: groups, isLoading } = useGroupList();
    const { activeID } = useRouteGroupSelection();
    const routeModels = useMemo(
        () => (groups ?? []).filter((group) => (group.route_group_id ?? DEFAULT_ROUTE_GROUP_ID) === activeID),
        [groups, activeID],
    );
    const pageKey = 'group' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const sortField = useToolbarViewOptionsStore((s) => s.getSortField(pageKey));
    const sortOrder = useToolbarViewOptionsStore((s) => s.getSortOrder(pageKey));
    const t = useTranslations('group');
    const [createOpen, setCreateOpen] = useState(false);
    const [autoGroupOpen, setAutoGroupOpen] = useState(false);
    const [dictOpen, setDictOpen] = useState(false);

    const sortedGroups = useMemo(() => {
        return [...routeModels].sort((a, b) => {
            if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1;
            if (a.pinned && b.pinned) {
                const ta = a.pinned_at ? new Date(a.pinned_at).getTime() : 0;
                const tb = b.pinned_at ? new Date(b.pinned_at).getTime() : 0;
                if (ta !== tb) return tb - ta;
            }
            const diff = sortField === 'name'
                ? a.name.localeCompare(b.name)
                : (a.id || 0) - (b.id || 0);
            return sortOrder === 'asc' ? diff : -diff;
        });
    }, [routeModels, sortField, sortOrder]);

    const visibleGroups = useMemo(() => {
        const term = searchTerm.toLowerCase().trim();
        return sortedGroups.filter((g) => {
            if (!term) return true;
            return g.name.toLowerCase().includes(term);
        });
    }, [sortedGroups, searchTerm]);

    const dialogs = (
        <>
            <MorphingDialog open={createOpen} onOpenChange={setCreateOpen}>
                <MorphingDialogContainer>
                    <MorphingDialogContent className="flex max-h-[calc(100vh-2rem)] w-fit max-w-full flex-col overflow-hidden rounded-3xl bg-card px-6 py-4 text-card-foreground custom-shadow">
                        <GroupCreateContent />
                    </MorphingDialogContent>
                </MorphingDialogContainer>
            </MorphingDialog>
            <MorphingDialog open={autoGroupOpen} onOpenChange={setAutoGroupOpen}>
                <MorphingDialogContainer>
                    <MorphingDialogContent className="flex max-h-[calc(100vh-2rem)] w-fit max-w-full flex-col overflow-hidden rounded-3xl bg-card px-6 py-4 text-card-foreground custom-shadow">
                        <GroupAutoGroupDialogContent />
                    </MorphingDialogContent>
                </MorphingDialogContainer>
            </MorphingDialog>
            <PublicModelDialog open={dictOpen} onOpenChange={setDictOpen} />
        </>
    );

    const shell = (body: React.ReactNode) => (
        <div className="flex h-full min-h-0 flex-col gap-3 pb-24 md:pb-4">
            <RouteGroupBar groups={groups ?? []} />
            <div className="min-h-0 flex-1">{body}</div>
            {dialogs}
        </div>
    );

    if (!isLoading && routeModels.length === 0) {
        return shell(
            <div className="flex h-full min-h-[20rem] items-center justify-center p-4">
                <div className="w-full max-w-lg rounded-3xl border border-dashed border-border/80 bg-card/70 p-8 text-center">
                    <div className="mx-auto mb-4 flex size-14 items-center justify-center rounded-2xl bg-primary/10 text-primary">
                        <Waypoints className="size-7" />
                    </div>
                    <h2 className="text-xl font-semibold text-foreground">{t('emptyState.title')}</h2>
                    <p className="mt-2 text-sm leading-6 text-muted-foreground">{t('emptyState.description')}</p>
                    <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:justify-center">
                        <Button className="rounded-2xl" onClick={() => setCreateOpen(true)}>
                            <Plus className="size-4" />
                            {t('emptyState.create')}
                        </Button>
                        <Button variant="outline" className="rounded-2xl" onClick={() => setDictOpen(true)}>
                            <BookMarked className="size-4" />
                            先建规范名
                        </Button>
                        <Button variant="outline" className="rounded-2xl" onClick={() => setAutoGroupOpen(true)}>
                            <Sparkles className="size-4" />
                            {t('emptyState.autoGroup')}
                        </Button>
                    </div>
                </div>
            </div>,
        );
    }

    if (visibleGroups.length === 0) {
        return shell(
            <div className="flex h-full min-h-[16rem] items-center justify-center rounded-3xl border border-dashed border-border/70 bg-muted/20 px-6 text-center text-sm text-muted-foreground">
                当前搜索下没有分组
            </div>,
        );
    }

    return shell(
        <VirtualizedGrid
            key={activeID}
            items={visibleGroups}
            columns={{ default: 1, md: 2, lg: 3 }}
            estimateItemHeight={520}
            getItemKey={(group, index) => group.id ?? `group-${index}`}
            renderItem={(group) => <GroupCard group={group} />}
        />,
    );
}
