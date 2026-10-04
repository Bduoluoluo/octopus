import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../client';

export const DEFAULT_ROUTE_GROUP_ID = 1;

export interface RouteGroup {
    id: number;
    name: string;
}

export function useRouteGroupList() {
    return useQuery({
        queryKey: ['route-groups'],
        queryFn: () => apiClient.get<RouteGroup[]>('/api/v1/route-group/list'),
        refetchInterval: 30000,
    });
}

export function useCreateRouteGroup() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: (name: string) => apiClient.post<RouteGroup>('/api/v1/route-group/create', { name }),
        onSuccess: (created) => {
            queryClient.setQueryData<RouteGroup[]>(['route-groups'], (current) => [...(current ?? []), created]);
            queryClient.invalidateQueries({ queryKey: ['route-groups'] });
        },
    });
}

export function useDeleteRouteGroup() {
    const queryClient = useQueryClient();
    return useMutation({
        mutationFn: (id: number) => apiClient.delete<null>(`/api/v1/route-group/delete/${id}`),
        onSuccess: (_, id) => {
            queryClient.setQueryData<RouteGroup[]>(['route-groups'], (current) => current?.filter((group) => group.id !== id));
            for (const queryKey of [['route-groups'], ['groups'], ['group-health'], ['runtime']]) {
                queryClient.invalidateQueries({ queryKey });
            }
        },
    });
}
