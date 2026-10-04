import { create } from 'zustand';
import { DEFAULT_ROUTE_GROUP_ID, useRouteGroupList } from '@/api/endpoints/route-group';

const useRouteGroupStore = create<{
    selectedID: number;
    select: (id: number) => void;
}>((set) => ({
    selectedID: DEFAULT_ROUTE_GROUP_ID,
    select: (selectedID) => set({ selectedID }),
}));

export function useRouteGroupSelection() {
    const selectedID = useRouteGroupStore((state) => state.selectedID);
    const select = useRouteGroupStore((state) => state.select);
    const query = useRouteGroupList();
    const activeID = query.data && !query.data.some((group) => group.id === selectedID)
        ? DEFAULT_ROUTE_GROUP_ID
        : selectedID;
    return { ...query, activeID, select };
}
